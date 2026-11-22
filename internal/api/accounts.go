// roomcord-bridge-host - A host process for the mautrix bridgev2 network connectors.
// Copyright (C) 2026 The roomcord-bridge-host authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, version 3 of the License.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/localmatrix"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

type account struct {
	key     string
	net     *hosts.Network
	loginID networkid.UserLoginID
	login   *bridgev2.UserLogin
}

func notFound(key string) error {
	return &login.Error{Status: 404, Code: "account_not_found", Message: fmt.Sprintf("unknown account %q", key)}
}

func (s *Server) resolve(r *http.Request, requireLogin bool) (*account, error) {
	key := r.PathValue("loginId")
	network, loginID, ok := login.ParseLoginKey(key)
	if !ok {
		return nil, notFound(key)
	}
	n := s.Reg.Get(network)
	if n == nil {
		return nil, notFound(key)
	}
	ul, err := n.Bridge.GetExistingUserLoginByID(r.Context(), loginID)
	if err != nil {
		return nil, err
	}
	if ul == nil && requireLogin {
		return nil, notFound(key)
	}
	return &account{key: key, net: n, loginID: loginID, login: ul}, nil
}

// accountState prefers the newest state that the connector queued: bridgev2 sends
// bridge states from a background loop, so the health map can lag by a moment.
func (s *Server) accountState(a *account) wire.Health {
	h, ok := a.net.Matrix.Health(a.key)
	if !ok {
		h = wire.Health{State: "connecting", Since: time.Now().UTC()}
	}
	if a.login != nil {
		latest := queuedState(a.login.BridgeState)
		if latest.StateEvent == "" {
			latest = a.login.BridgeState.GetPrev()
		}
		if latest.StateEvent != "" {
			if st, _ := localmatrix.MapBridgeState(latest.StateEvent); st != h.State {
				h = wire.Health{State: st, Since: time.Now().UTC()}
			}
		}
	}
	return h
}

// active resolves a login that can serve data calls: 404 account_not_found when
// unknown, 409 session_revoked when the network ended the session.
func (s *Server) active(r *http.Request) (*account, error) {
	a, err := s.resolve(r, true)
	if err != nil {
		return nil, err
	}
	if s.accountState(a).State == "needs_reauth" {
		return nil, &login.Error{Status: 409, Code: "session_revoked", Message: "the network ended the session, log in again"}
	}
	return a, nil
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		writeError(w, 400, "invalid_input", "userId is required")
		return
	}
	out := []wire.Account{}
	for _, netID := range s.Reg.IDs() {
		n := s.Reg.Get(netID)
		rows, err := n.Bridge.DB.UserLogin.GetAllForUser(r.Context(), localmatrix.UserMXID(userID))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, row := range rows {
			a := &account{key: n.Matrix.LoginKey(row.ID), net: n, loginID: row.ID}
			a.login = n.Bridge.GetCachedUserLoginByID(row.ID)
			out = append(out, wire.Account{
				LoginID:    a.key,
				Network:    netID,
				UserID:     userID,
				RemoteID:   string(row.ID),
				RemoteName: row.RemoteName,
				State:      s.accountState(a).State,
			})
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) accountHealth(w http.ResponseWriter, r *http.Request) {
	a, err := s.resolve(r, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, s.accountState(a))
}

type subscriber struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	a, err := s.resolve(r, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st := s.Reg.Store
	head, err := st.HeadSeq(r.Context(), a.key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if a.login == nil && head == 0 {
		s.fail(w, r, notFound(a.key))
		return
	}
	var after int64
	rawCursor := r.URL.Query().Get("cursor")
	sendCheckpoint := rawCursor == "" || rawCursor == "null"
	if sendCheckpoint {
		after = head
	} else if _, err := fmt.Sscan(rawCursor, &after); err != nil || after < 0 {
		writeError(w, 400, "invalid_input", "cursor must be a non-negative integer")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	sub := &subscriber{conn: conn, cancel: cancel}
	s.subsMu.Lock()
	if old := s.subs[a.key]; old != nil {
		go func() {
			_ = old.conn.Close(websocket.StatusCode(4000), "replaced by a newer subscriber")
			old.cancel()
		}()
	}
	s.subs[a.key] = sub
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		if s.subs[a.key] == sub {
			delete(s.subs, a.key)
		}
		s.subsMu.Unlock()
		cancel()
	}()
	ctx = conn.CloseRead(ctx)
	log := zerolog.Ctx(r.Context()).With().Str("login", a.key).Logger()

	if sendCheckpoint {
		if err = wsjson.Write(ctx, conn, wire.Frame{Seq: head, Type: wire.EventCheckpoint, Payload: map[string]any{}}); err != nil {
			return
		}
	}
	for {
		changed := st.Changed(a.key)
		frames, err := st.ReadEvents(ctx, a.key, after, 100)
		if err != nil {
			if ctx.Err() == nil {
				log.Err(err).Msg("Failed to read outbox")
				_ = conn.Close(websocket.StatusInternalError, "outbox read failed")
			}
			return
		}
		for _, f := range frames {
			if f.Type == wire.EventAccountStatus && s.staleStatus(r.Context(), a, f) {
				after = f.Seq
				continue
			}
			if err = wsjson.Write(ctx, conn, f); err != nil {
				if !isNormalClose(err) {
					log.Debug().Err(err).Msg("Event stream write failed")
				}
				return
			}
			after = f.Seq
		}
		if len(frames) > 0 {
			continue
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Second):
			pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
			err = conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		}
	}
}

// staleStatus reports an account_status frame whose status is no longer the current
// one, e.g. a needs_reauth that a later re-login replaced. Such frames are skipped.
func (s *Server) staleStatus(ctx context.Context, a *account, f wire.Frame) bool {
	raw, ok := f.Payload.(json.RawMessage)
	if !ok {
		return false
	}
	var p wire.AccountStatusPayload
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	current := &account{key: a.key, net: a.net, loginID: a.loginID, login: a.net.Bridge.GetCachedUserLoginByID(a.loginID)}
	return s.accountState(current).State != p.Status
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	a, err := s.active(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	items, next, err := s.Reg.Store.ListConversations(r.Context(), a.key, q.Get("cursor"), pageLimit(q.Get("limit"), 50, 200))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, wire.Page[json.RawMessage]{Items: items, NextCursor: next})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	a, err := s.active(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		ConversationID string `json:"conversationId"`
		Cursor         string `json:"cursor"`
		Limit          int    `json:"limit"`
	}
	if err = decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	exists, err := s.Reg.Store.ConversationExists(r.Context(), a.key, req.ConversationID)
	if err != nil {
		s.fail(w, r, err)
		return
	} else if !exists {
		writeError(w, 404, "conversation_not_found", "unknown conversation")
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items, next, err := s.Reg.Store.History(r.Context(), a.key, req.ConversationID, req.Cursor, limit)
	if err != nil {
		writeError(w, 400, "invalid_input", err.Error())
		return
	}
	writeJSON(w, 200, wire.Page[json.RawMessage]{Items: items, NextCursor: next})
}

func (s *Server) portalFor(ctx context.Context, a *account, conversationID string) (*bridgev2.Portal, error) {
	portal, err := a.net.Bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: networkid.PortalID(conversationID), Receiver: a.loginID})
	if err != nil {
		return nil, err
	}
	if portal == nil || portal.MXID == "" {
		return nil, &login.Error{Status: 404, Code: "conversation_not_found", Message: "unknown conversation"}
	}
	return portal, nil
}

func (s *Server) findMessage(ctx context.Context, a *account, extID string) (*database.Message, error) {
	msgID, partID := localmatrix.SplitExternalMessageID(extID)
	var msg *database.Message
	var err error
	if partID != "" {
		msg, err = a.net.Bridge.DB.Message.GetPartByID(ctx, a.loginID, msgID, partID)
	} else {
		msg, err = a.net.Bridge.DB.Message.GetFirstPartByID(ctx, a.loginID, msgID)
	}
	if err != nil {
		return nil, err
	} else if msg == nil {
		return nil, &login.Error{Status: 404, Code: "message_not_found", Message: "unknown message"}
	}
	return msg, nil
}

type inflightSend struct {
	done chan struct{}
	resp *wire.SendResponse
	err  error
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	a, err := s.active(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		ConversationID   string   `json:"conversationId"`
		ClientMessageID  string   `json:"clientMessageId"`
		Text             string   `json:"text"`
		UploadRefs       []string `json:"uploadRefs"`
		ReplyToMessageID string   `json:"replyToMessageId"`
	}
	if err = decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	req.ClientMessageID = cleanID(req.ClientMessageID)
	if req.ClientMessageID == "" || req.ConversationID == "" || (req.Text == "" && len(req.UploadRefs) == 0) {
		writeError(w, 400, "invalid_input", "conversationId, clientMessageId and text or uploadRefs are required")
		return
	}
	if len(req.UploadRefs) > 1 {
		writeError(w, 422, "unsupported_operation", "the host sends at most one upload per message")
		return
	}
	flightKey := a.key + "\x00" + req.ClientMessageID
	s.sendsMu.Lock()
	if f := s.sends[flightKey]; f != nil {
		s.sendsMu.Unlock()
		<-f.done
		if f.err != nil {
			s.fail(w, r, f.err)
		} else {
			writeJSON(w, 200, f.resp)
		}
		return
	}
	f := &inflightSend{done: make(chan struct{})}
	s.sends[flightKey] = f
	s.sendsMu.Unlock()
	f.resp, f.err = s.doSend(r.Context(), a, req.ConversationID, req.ClientMessageID, req.Text, req.UploadRefs, req.ReplyToMessageID)
	close(f.done)
	s.sendsMu.Lock()
	delete(s.sends, flightKey)
	s.sendsMu.Unlock()
	if f.err != nil {
		s.fail(w, r, f.err)
		return
	}
	writeJSON(w, 200, f.resp)
}

func (s *Server) doSend(ctx context.Context, a *account, convID, clientMessageID, text string, uploadRefs []string, replyTo string) (*wire.SendResponse, error) {
	st := s.Reg.Store
	if prev, err := st.GetSend(ctx, a.key, clientMessageID); err != nil || prev != nil {
		return prev, err
	}
	portal, err := s.portalFor(ctx, a, convID)
	if err != nil {
		return nil, err
	}
	br := a.net.Bridge
	evtID := localmatrix.OutgoingEventID(a.key, clientMessageID)
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: text}
	if len(uploadRefs) == 1 {
		media, err := st.GetMedia(ctx, uploadRefs[0])
		if err != nil {
			return nil, err
		} else if media == nil || media.LoginID != a.key {
			return nil, &login.Error{Status: 404, Code: "attachment_not_found", Message: "unknown uploadRef"}
		}
		content.MsgType = msgTypeForMime(media.MimeType)
		content.URL = id.ContentURIString("mxc://" + localmatrix.ServerName + "/" + media.ID)
		content.FileName = media.FileName
		if content.Body == "" {
			content.Body = media.FileName
		}
		content.Info = &event.FileInfo{MimeType: media.MimeType, Size: int(media.Size)}
	}
	if replyTo != "" {
		target, err := s.findMessage(ctx, a, replyTo)
		if err != nil {
			return nil, err
		}
		content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(target.MXID)
	}
	existing, err := br.DB.Message.GetPartByMXID(ctx, evtID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		statusCh, stopWaiting := a.net.Matrix.WaitForStatus(evtID)
		defer stopWaiting()
		evt := &event.Event{
			Sender:    a.login.UserMXID,
			Type:      event.EventMessage,
			Timestamp: time.Now().UnixMilli(),
			ID:        evtID,
			RoomID:    portal.MXID,
			Content:   event.Content{Parsed: content},
		}
		res := br.QueueMatrixEvent(zerolog.Ctx(ctx).WithContext(br.BackgroundCtx), evt)
		if !res.Success && !res.Queued && !res.Ignored {
			return nil, sendError(res.Error)
		}
		timeout := time.NewTimer(60 * time.Second)
		defer timeout.Stop()
		select {
		case upd := <-statusCh:
			if upd.Status.Status != event.MessageStatusSuccess {
				msg := upd.Status.Message
				if msg == "" && upd.Status.InternalError != nil {
					msg = upd.Status.InternalError.Error()
				}
				return nil, sendError(errors.New(msg))
			}
			if upd.Info.NewEventID != "" {
				evtID = upd.Info.NewEventID
			}
		case <-timeout.C:
			return nil, &login.Error{Status: 504, Code: "network_unavailable", Message: "the network did not confirm the message in time"}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		existing, err = br.DB.Message.GetPartByMXID(ctx, evtID)
		if err != nil {
			return nil, err
		} else if existing == nil {
			return nil, &login.Error{Status: 500, Code: "internal_error", Message: "message was sent but not stored"}
		}
	}
	msg := a.net.Matrix.OutgoingMessage(ctx, a.loginID, convID, existing, content)
	resp := &wire.SendResponse{
		ExternalMessageID: localmatrix.ExternalMessageID(existing.ID, existing.PartID),
		SentAt:            existing.Timestamp.UTC().Truncate(time.Millisecond),
	}
	if len(msg.Attachments) > 0 {
		resp.Attachments = msg.Attachments
	}
	if err = st.PutSend(ctx, a.key, clientMessageID, resp); err != nil {
		return nil, err
	}
	if _, err = st.Emit(ctx, a.key, wire.EventMessageUpsert, &wire.MessageUpsertPayload{Message: msg}); err != nil {
		return nil, err
	}
	return resp, nil
}

func sendError(err error) error {
	msg := "send failed"
	if err != nil {
		msg = err.Error()
	}
	code := "network_unavailable"
	if errors.Is(err, bridgev2.ErrNotLoggedIn) || strings.Contains(msg, "not logged in") {
		code = "account_not_connected"
	}
	return &login.Error{Status: 502, Code: code, Message: msg}
}

func msgTypeForMime(mime string) event.MessageType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mime, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mime, "audio/"):
		return event.MsgAudio
	default:
		return event.MsgFile
	}
}

func (s *Server) read(w http.ResponseWriter, r *http.Request) {
	a, err := s.active(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		ConversationID string `json:"conversationId"`
		UpToMessageID  string `json:"upToMessageId"`
	}
	if err = decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	portal, err := s.portalFor(r.Context(), a, req.ConversationID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var target *database.Message
	if req.UpToMessageID != "" {
		target, err = s.findMessage(r.Context(), a, req.UpToMessageID)
	} else {
		target, err = a.net.Bridge.DB.Message.GetLastPartAtOrBeforeTime(r.Context(), portal.PortalKey, time.Now())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	} else if target == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	evt := &event.Event{
		Type:   event.EphemeralEventReceipt,
		RoomID: portal.MXID,
		Content: event.Content{Parsed: &event.ReceiptEventContent{
			target.MXID: {event.ReceiptTypeRead: {a.login.UserMXID: {Timestamp: time.Now()}}},
		}},
	}
	evt.Mautrix.EventSource = event.SourceEphemeral
	a.net.Bridge.QueueMatrixEvent(zerolog.Ctx(r.Context()).WithContext(a.net.Bridge.BackgroundCtx), evt)
	w.WriteHeader(http.StatusNoContent)
}

const maxUpload = 100 << 20

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	a, err := s.active(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "invalid_input", "multipart field \"file\" is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, 413, "attachment_too_large", err.Error())
		return
	}
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	uri, err := localmatrix.StoreMedia(r.Context(), s.Reg.Store, s.Reg.MediaDir, a.key, data, header.Filename, mime)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	parsed, _ := uri.Parse()
	writeJSON(w, 200, map[string]string{"uploadRef": parsed.FileID})
}

func (s *Server) downloadMedia(w http.ResponseWriter, r *http.Request) {
	a, err := s.resolve(r, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	attID := r.PathValue("attachmentId")
	media, err := s.Reg.Store.GetMedia(r.Context(), attID)
	if err != nil {
		s.fail(w, r, err)
		return
	} else if media == nil || media.LoginID != a.key || strings.ContainsAny(attID, "/\\.") {
		writeError(w, 404, "attachment_not_found", "unknown attachment")
		return
	}
	w.Header().Set("Content-Type", media.MimeType)
	if media.FileName != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": media.FileName}))
	}
	http.ServeFile(w, r, filepath.Join(s.Reg.MediaDir, media.ID))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	a, err := s.resolve(r, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a.login.Logout(zerolog.Ctx(r.Context()).WithContext(context.WithoutCancel(r.Context())))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteData(w http.ResponseWriter, r *http.Request) {
	a, err := s.resolve(r, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := zerolog.Ctx(r.Context()).WithContext(context.WithoutCancel(r.Context()))
	portals, err := a.net.Bridge.GetAllPortals(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, p := range portals {
		if p.Receiver == a.loginID {
			if err = p.Delete(ctx); err != nil {
				s.fail(w, r, err)
				return
			}
		}
	}
	mediaIDs, err := s.Reg.Store.DeleteLogin(ctx, a.key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, m := range mediaIDs {
		_ = os.Remove(filepath.Join(s.Reg.MediaDir, m))
	}
	if a.login == nil {
		a.net.Matrix.ForgetHealth(a.key)
	}
	w.WriteHeader(http.StatusNoContent)
}
