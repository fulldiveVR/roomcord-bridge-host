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

package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

type ctxKey int

const sessionKey ctxKey = 1

// Handler serves the network API (/api/v1), the control API (/control), the web
// login pages (/web) and the phone page (/phone).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "name": "fakechat"})
	})

	auth := func(scope string, h http.HandlerFunc) http.Handler { return s.withFault(scope, http.HandlerFunc(h)) }
	mux.Handle("POST /api/v1/auth/qr", auth(proto.ScopeAuth, s.qrStart))
	mux.Handle("GET /api/v1/auth/qr/{qrId}", auth(proto.ScopeAuth, s.qrPoll))
	mux.Handle("DELETE /api/v1/auth/qr/{qrId}", auth(proto.ScopeAuth, s.qrCancel))
	mux.Handle("POST /api/v1/auth/phone", auth(proto.ScopeAuth, s.phoneStart))
	mux.Handle("POST /api/v1/auth/phone/verify", auth(proto.ScopeAuth, s.phoneVerify))
	mux.Handle("POST /api/v1/auth/password", auth(proto.ScopeAuth, s.passwordLogin))
	mux.Handle("POST /api/v1/auth/2fa", auth(proto.ScopeAuth, s.twoFactor))
	mux.Handle("POST /api/v1/auth/web", auth(proto.ScopeAuth, s.webExchange))

	api := func(scope string, h http.HandlerFunc) http.Handler { return s.withFault(scope, s.requireSession(h)) }
	mux.Handle("GET /api/v1/me", api(proto.ScopeAny, s.me))
	mux.Handle("POST /api/v1/logout", api(proto.ScopeAny, s.logout))
	mux.Handle("GET /api/v1/users/{userId}", api(proto.ScopeHistory, s.getUser))
	mux.Handle("GET /api/v1/chats", api(proto.ScopeHistory, s.listChats))
	mux.Handle("GET /api/v1/chats/{chatId}", api(proto.ScopeHistory, s.getChat))
	mux.Handle("GET /api/v1/chats/{chatId}/messages", api(proto.ScopeHistory, s.listMessages))
	mux.Handle("POST /api/v1/chats/{chatId}/messages", api(proto.ScopeSend, s.sendMessage))
	mux.Handle("PATCH /api/v1/chats/{chatId}/messages/{messageId}", api(proto.ScopeSend, s.editMessage))
	mux.Handle("DELETE /api/v1/chats/{chatId}/messages/{messageId}", api(proto.ScopeSend, s.deleteMessage))
	mux.Handle("PUT /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}", api(proto.ScopeSend, s.react))
	mux.Handle("DELETE /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}", api(proto.ScopeSend, s.unreact))
	mux.Handle("POST /api/v1/chats/{chatId}/read", api(proto.ScopeSend, s.markRead))
	mux.Handle("POST /api/v1/chats/{chatId}/typing", api(proto.ScopeSend, s.typing))
	mux.Handle("POST /api/v1/media", api(proto.ScopeMedia, s.uploadMedia))
	mux.Handle("GET /api/v1/media/{mediaId}", api(proto.ScopeMedia, s.downloadMedia))
	mux.Handle("GET /api/v1/events", api(proto.ScopeEvents, s.events))

	s.controlRoutes(mux)
	s.pageRoutes(mux)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, proto.ErrorBody{Error: proto.Error{Code: code, Message: msg}})
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(into); err != nil {
		writeErr(w, 400, proto.ErrInvalidInput, "invalid JSON body")
		return false
	}
	return true
}

func (s *Server) withFault(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := s.takeFault(scope); f != nil {
			if f.Kind == proto.FaultRateLimit {
				w.Header().Set("Retry-After", "1")
				writeJSON(w, 429, proto.ErrorBody{Error: proto.Error{Code: proto.ErrRateLimited, Message: "slow down", RetryAfter: 1}})
			} else {
				writeErr(w, 500, proto.ErrServerError, "injected server error")
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok
}

func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		sess := s.sessions[bearer(r)]
		s.mu.Unlock()
		if sess == nil {
			writeErr(w, 401, proto.ErrUnauthorized, "the session is not valid")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	}
}

func sessionOf(r *http.Request) *session { return r.Context().Value(sessionKey).(*session) }

// acct returns the caller's account under s.mu; the caller must unlock. nil when
// the account is gone (reset) and the error is written.
func (s *Server) acct(w http.ResponseWriter, r *http.Request) (*account, *session) {
	sess := sessionOf(r)
	s.mu.Lock()
	a := s.accounts[sess.accountID]
	if a == nil || s.sessions[sess.token] == nil {
		s.mu.Unlock()
		writeErr(w, 401, proto.ErrUnauthorized, "the session is not valid")
		return nil, nil
	}
	return a, sess
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func constantEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// ---- auth

func (s *Server) qrStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	q := &qrCode{PendingQR: proto.PendingQR{QRID: s.nextID("qr"), ExpiresAt: time.Now().Add(s.cfg.QRTTL).UTC()}}
	q.Data = "fakechat://link?qr=" + q.QRID + "&s=" + s.secret("")[:8]
	s.qrs[q.QRID] = q
	s.mu.Unlock()
	writeJSON(w, 200, proto.QRStart{QRID: q.QRID, Data: q.Data, ExpiresAt: q.ExpiresAt, PhoneURL: s.baseURL(r) + "/phone"})
}

// qrPoll answers at once, or waits up to ?wait= seconds (max 30) for a change.
func (s *Server) qrPoll(w http.ResponseWriter, r *http.Request) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	deadline := time.Now().Add(time.Duration(min(max(wait, 0), 30)) * time.Second)
	id := r.PathValue("qrId")
	for {
		s.mu.Lock()
		q := s.qrs[id]
		var out proto.QRStatus
		switch {
		case q == nil:
			s.mu.Unlock()
			writeErr(w, 404, proto.ErrNotFound, "unknown QR code")
			return
		case q.approvedBy != "":
			a := s.accounts[q.approvedBy]
			delete(s.qrs, id)
			if a == nil {
				out.Status = proto.QRExpired
			} else {
				sess := s.newSession(a)
				out = proto.QRStatus{Status: proto.QRApproved, Session: &sess}
			}
		case time.Now().After(q.ExpiresAt):
			delete(s.qrs, id)
			out.Status = proto.QRExpired
		default:
			out.Status = proto.QRPending
		}
		s.mu.Unlock()
		if out.Status != proto.QRPending || !time.Now().Before(deadline) {
			writeJSON(w, 200, out)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// qrCancel drops a code that is not approved yet, so the phone page stops offering it.
func (s *Server) qrCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if q := s.qrs[r.PathValue("qrId")]; q != nil && q.approvedBy == "" {
		delete(s.qrs, q.QRID)
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// ApproveQR approves a pending QR code for an account, like a scan on the phone.
func (s *Server) ApproveQR(qrID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.qrs[qrID]
	if q == nil || time.Now().After(q.ExpiresAt) {
		return errors.New("unknown or expired QR code")
	}
	if s.accounts[accountID] == nil {
		return errors.New("unknown account")
	}
	q.approvedBy = accountID
	return nil
}

var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func (s *Server) phoneStart(w http.ResponseWriter, r *http.Request) {
	var req proto.PhoneStart
	if !decode(w, r, &req) {
		return
	}
	phone := strings.ReplaceAll(strings.TrimSpace(req.Phone), " ", "")
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	if !phoneRe.MatchString(phone) || strings.HasPrefix(phone, InvalidPhonePrefix) {
		writeErr(w, 400, proto.ErrInvalidPhone, "the phone number is not valid")
		return
	}
	s.mu.Lock()
	var a *account
	for _, acc := range s.accounts {
		if acc.Phone == phone {
			a = acc
		}
	}
	if a == nil {
		var err error
		a, err = s.createAccountLocked(proto.CreateAccount{ID: "p" + strings.TrimPrefix(phone, "+"), DisplayName: "Phone " + phone, Phone: phone})
		if err != nil {
			s.mu.Unlock()
			writeErr(w, 400, proto.ErrInvalidPhone, err.Error())
			return
		}
	}
	c := &challenge{id: s.nextID("ch"), accountID: a.ID, attempts: CodeAttempts, kind: "phone"}
	s.challenges[c.id] = c
	s.mu.Unlock()
	writeJSON(w, 200, proto.Challenge{ChallengeID: c.id})
}

func (s *Server) checkCode(w http.ResponseWriter, r *http.Request, kind string, expected func(*account) string) {
	var req proto.CodeSubmit
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.challenges[req.ChallengeID]
	if c == nil || c.kind != kind {
		writeErr(w, 410, proto.ErrChallengeExpired, "the code request expired, start again")
		return
	}
	a := s.accounts[c.accountID]
	if a == nil {
		delete(s.challenges, c.id)
		writeErr(w, 410, proto.ErrChallengeExpired, "the account is gone")
		return
	}
	if !constantEq(strings.TrimSpace(req.Code), expected(a)) {
		c.attempts--
		if c.attempts <= 0 {
			delete(s.challenges, c.id)
			writeErr(w, 410, proto.ErrChallengeExpired, "too many wrong codes, start again")
			return
		}
		left := c.attempts
		writeJSON(w, 401, proto.ErrorBody{Error: proto.Error{Code: proto.ErrInvalidCode, Message: "the code is wrong", AttemptsLeft: &left}})
		return
	}
	delete(s.challenges, c.id)
	writeJSON(w, 200, s.newSession(a))
}

func (s *Server) phoneVerify(w http.ResponseWriter, r *http.Request) {
	s.checkCode(w, r, "phone", func(*account) string { return SMSCode })
}

func (s *Server) twoFactor(w http.ResponseWriter, r *http.Request) {
	s.checkCode(w, r, "2fa", func(a *account) string { return a.twoFactor })
}

func (s *Server) findByUsername(username string) *account {
	for _, a := range s.accounts {
		if a.Username != "" && strings.EqualFold(a.Username, username) {
			return a
		}
	}
	return nil
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var req proto.PasswordLogin
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findByUsername(strings.TrimSpace(req.Username))
	if a == nil || a.password == "" || !constantEq(req.Password, a.password) {
		writeErr(w, 401, proto.ErrInvalidPassword, "wrong username or password")
		return
	}
	if a.twoFactor != "" {
		c := &challenge{id: s.nextID("ch"), accountID: a.ID, attempts: CodeAttempts, kind: "2fa"}
		s.challenges[c.id] = c
		writeJSON(w, 200, proto.PasswordResult{TwoFactorRequired: true, ChallengeID: c.id})
		return
	}
	writeJSON(w, 200, proto.PasswordResult{Session: s.newSession(a)})
}

func (s *Server) webExchange(w http.ResponseWriter, r *http.Request) {
	var req proto.WebExchange
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.accounts[s.webSessions[req.Session]]
	if a == nil {
		writeErr(w, 401, proto.ErrInvalidSession, "the web session is not valid or expired")
		return
	}
	writeJSON(w, 200, s.newSession(a))
}

// ---- account API

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	a, _ := s.acct(w, r)
	if a == nil {
		return
	}
	out := a.Account
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	a, sess := s.acct(w, r)
	if a == nil {
		return
	}
	delete(s.sessions, sess.token)
	var streams []*stream
	for st := range a.streams {
		if st.token == sess.token {
			streams = append(streams, st)
		}
	}
	s.mu.Unlock()
	for _, st := range streams {
		kick(st, proto.CloseLoggedOut)
	}
	w.WriteHeader(204)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	a, _ := s.acct(w, r)
	if a == nil {
		return
	}
	u, ok := s.user(r.PathValue("userId"))
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, proto.ErrNotFound, "unknown user")
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	a, _ := s.acct(w, r)
	if a == nil {
		return
	}
	out := []proto.Chat{}
	for _, c := range sortedChats(a) {
		out = append(out, c.view())
	}
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

// chatOf returns the chat under s.mu (still locked) or writes 404 and unlocks.
func (s *Server) chatOf(w http.ResponseWriter, r *http.Request) (*account, *session, *chat) {
	a, sess := s.acct(w, r)
	if a == nil {
		return nil, nil, nil
	}
	c := a.chats[r.PathValue("chatId")]
	if c == nil {
		s.mu.Unlock()
		writeErr(w, 404, proto.ErrNotFound, "unknown chat")
		return nil, nil, nil
	}
	return a, sess, c
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	_, _, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	out := c.view()
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

func copyMessage(m *proto.Message) proto.Message {
	out := *m
	out.Entities = append([]proto.Entity(nil), m.Entities...)
	out.Reactions = append([]proto.Reaction(nil), m.Reactions...)
	return out
}

// listMessages pages a chat: newest `limit` messages, or the ones before/after an id.
// Messages are oldest first in every page.
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	_, _, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	defer s.mu.Unlock()
	index := func(id string) int {
		for i, m := range c.messages {
			if m.ID == id {
				return i
			}
		}
		return -1
	}
	lo, hi := max(len(c.messages)-limit, 0), len(c.messages)
	hasMore := lo > 0
	if before := q.Get("before"); before != "" {
		i := index(before)
		if i < 0 {
			writeErr(w, 404, proto.ErrNotFound, "unknown message")
			return
		}
		lo, hi = max(i-limit, 0), i
		hasMore = lo > 0
	} else if after := q.Get("after"); after != "" {
		i := index(after)
		if i < 0 {
			writeErr(w, 404, proto.ErrNotFound, "unknown message")
			return
		}
		lo, hi = i+1, min(i+1+limit, len(c.messages))
		hasMore = hi < len(c.messages)
	}
	page := proto.MessagePage{Messages: []proto.Message{}, HasMore: hasMore}
	for _, m := range c.messages[lo:hi] {
		page.Messages = append(page.Messages, copyMessage(m))
	}
	writeJSON(w, 200, page)
}

func kindForMime(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return proto.KindImage
	case strings.HasPrefix(mime, "video/"):
		return proto.KindVideo
	case strings.HasPrefix(mime, "audio/"):
		return proto.KindAudio
	default:
		return proto.KindFile
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req proto.SendMessage
	if !decode(w, r, &req) {
		return
	}
	a, sess, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	defer s.mu.Unlock()
	if req.ClientID != "" {
		if id := a.clientIDs[req.ClientID]; id != "" && c.byID[id] != nil {
			writeJSON(w, 200, copyMessage(c.byID[id]))
			return
		}
	}
	m := &proto.Message{ID: s.nextID("msg"), ChatID: c.ID, SenderID: a.ID, TS: s.now(), Kind: proto.KindText,
		Text: req.Text, Entities: req.Entities, ReplyTo: req.ReplyTo}
	if req.ReplyTo != "" && c.byID[req.ReplyTo] == nil {
		writeErr(w, 400, proto.ErrInvalidInput, "replyTo is not a message of this chat")
		return
	}
	rec := proto.SentRecord{Action: proto.SentSend, ChatID: c.ID, ClientID: req.ClientID, Text: req.Text, Entities: req.Entities, ReplyTo: req.ReplyTo}
	switch {
	case req.MediaID != "":
		sm := s.media[req.MediaID]
		if sm == nil {
			writeErr(w, 400, proto.ErrInvalidInput, "unknown mediaId")
			return
		}
		meta := sm.meta
		m.Kind = meta.Kind
		if req.Kind != "" {
			m.Kind, meta.Kind = req.Kind, req.Kind
		}
		m.Media = &meta
		rec.MediaID, rec.MediaName, rec.Mime, rec.Size = meta.ID, meta.Name, meta.Mime, meta.Size
	case req.Location != nil:
		m.Kind = proto.KindLocation
		m.Location = req.Location
	case strings.TrimSpace(req.Text) == "":
		writeErr(w, 400, proto.ErrInvalidInput, "text, mediaId or location is required")
		return
	}
	rec.Kind = m.Kind
	c.append(m)
	if req.ClientID != "" {
		a.clientIDs[req.ClientID] = m.ID
	}
	rec.MessageID = m.ID
	s.record(a, rec)
	out := copyMessage(m)
	s.emit(a, proto.Event{Type: proto.EventMessage, ChatID: c.ID, Message: &out, TS: m.TS}, sess.token)
	writeJSON(w, 200, out)
}

func (s *Server) ownMessage(w http.ResponseWriter, r *http.Request, a *account, c *chat) *proto.Message {
	m := c.byID[r.PathValue("messageId")]
	if m == nil || m.Deleted {
		writeErr(w, 404, proto.ErrNotFound, "unknown message")
		return nil
	}
	if m.SenderID != a.ID {
		writeErr(w, 403, proto.ErrInvalidInput, "only the sender can change a message")
		return nil
	}
	return m
}

func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	var req proto.EditMessage
	if !decode(w, r, &req) {
		return
	}
	a, sess, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	defer s.mu.Unlock()
	m := s.ownMessage(w, r, a, c)
	if m == nil {
		return
	}
	ts := s.now()
	m.Text, m.Entities, m.EditedAt = req.Text, req.Entities, &ts
	s.record(a, proto.SentRecord{Action: proto.SentEdit, ChatID: c.ID, MessageID: m.ID, Text: req.Text, Entities: req.Entities})
	out := copyMessage(m)
	s.emit(a, proto.Event{Type: proto.EventMessageEdit, ChatID: c.ID, Message: &out, MessageID: m.ID, TS: ts}, sess.token)
	writeJSON(w, 200, out)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	a, sess, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	defer s.mu.Unlock()
	m := s.ownMessage(w, r, a, c)
	if m == nil {
		return
	}
	deleteContent(m)
	s.record(a, proto.SentRecord{Action: proto.SentDelete, ChatID: c.ID, MessageID: m.ID})
	s.emit(a, proto.Event{Type: proto.EventMessageDelete, ChatID: c.ID, MessageID: m.ID, UserID: a.ID}, sess.token)
	w.WriteHeader(204)
}

func deleteContent(m *proto.Message) {
	m.Deleted = true
	m.Text, m.Entities, m.Media, m.Location, m.Contact, m.Reactions = "", nil, nil, nil, nil, nil
}

func setReaction(m *proto.Message, userID, emoji string, add bool, at time.Time) bool {
	for i, rc := range m.Reactions {
		if rc.UserID == userID && rc.Emoji == emoji {
			if add {
				return false
			}
			m.Reactions = append(m.Reactions[:i], m.Reactions[i+1:]...)
			return true
		}
	}
	if !add {
		return false
	}
	m.Reactions = append(m.Reactions, proto.Reaction{UserID: userID, Emoji: emoji, At: at})
	return true
}

func (s *Server) reaction(w http.ResponseWriter, r *http.Request, add bool) {
	a, sess, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	defer s.mu.Unlock()
	m := c.byID[r.PathValue("messageId")]
	emoji := r.PathValue("emoji")
	if m == nil || m.Deleted {
		writeErr(w, 404, proto.ErrNotFound, "unknown message")
		return
	} else if emoji == "" {
		writeErr(w, 400, proto.ErrInvalidInput, "emoji is required")
		return
	}
	ts := s.now()
	action, typ := proto.SentReact, proto.EventReactionAdd
	if !add {
		action, typ = proto.SentUnreact, proto.EventReactionRemove
	}
	s.record(a, proto.SentRecord{Action: action, ChatID: c.ID, MessageID: m.ID, Emoji: emoji})
	if setReaction(m, a.ID, emoji, add, ts) {
		s.emit(a, proto.Event{Type: typ, ChatID: c.ID, MessageID: m.ID, UserID: a.ID, Emoji: emoji, TS: ts}, sess.token)
	}
	w.WriteHeader(204)
}

func (s *Server) react(w http.ResponseWriter, r *http.Request)   { s.reaction(w, r, true) }
func (s *Server) unreact(w http.ResponseWriter, r *http.Request) { s.reaction(w, r, false) }

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	var req proto.ReadBody
	if !decode(w, r, &req) {
		return
	}
	a, sess, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	defer s.mu.Unlock()
	if req.MessageID == "" {
		req.MessageID = c.LastMessageID
	}
	if c.byID[req.MessageID] == nil {
		writeErr(w, 404, proto.ErrNotFound, "unknown message")
		return
	}
	s.record(a, proto.SentRecord{Action: proto.SentRead, ChatID: c.ID, MessageID: req.MessageID})
	s.emit(a, proto.Event{Type: proto.EventRead, ChatID: c.ID, MessageID: req.MessageID, UserID: a.ID}, sess.token)
	w.WriteHeader(204)
}

func (s *Server) typing(w http.ResponseWriter, r *http.Request) {
	var req proto.TypingBody
	if !decode(w, r, &req) {
		return
	}
	a, _, c := s.chatOf(w, r)
	if c == nil {
		return
	}
	s.record(a, proto.SentRecord{Action: proto.SentTyping, ChatID: c.ID, Typing: req.Typing})
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, 413, proto.ErrMediaTooLarge, "the file is over 64 MiB")
			return
		}
		writeErr(w, 400, proto.ErrInvalidInput, "multipart field file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxUpload+1))
	if err != nil || len(data) > MaxUpload {
		writeErr(w, 413, proto.ErrMediaTooLarge, "the file is over 64 MiB")
		return
	}
	mime := header.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		if detected := http.DetectContentType(data); detected != "application/octet-stream" {
			mime = detected
		}
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	q := r.URL.Query()
	width, _ := strconv.Atoi(q.Get("width"))
	height, _ := strconv.Atoi(q.Get("height"))
	duration, _ := strconv.Atoi(q.Get("durationMs"))
	a, _ := s.acct(w, r)
	if a == nil {
		return
	}
	defer s.mu.Unlock()
	kind := kindForMime(mime)
	if k := q.Get("kind"); k != "" {
		kind = k
	}
	meta := proto.Media{ID: s.nextID("up"), Kind: kind, Name: header.Filename, Mime: mime, Size: int64(len(data)),
		Width: width, Height: height, DurationMs: duration}
	s.media[meta.ID] = &storedMedia{meta: meta, data: func() []byte { return data }}
	s.record(a, proto.SentRecord{Action: proto.SentUpload, MediaID: meta.ID, MediaName: meta.Name, Mime: mime, Size: meta.Size, Kind: kind})
	writeJSON(w, 200, meta)
}

func (s *Server) downloadMedia(w http.ResponseWriter, r *http.Request) {
	a, _ := s.acct(w, r)
	if a == nil {
		return
	}
	sm := s.media[r.PathValue("mediaId")]
	s.mu.Unlock()
	if sm == nil {
		writeErr(w, 404, proto.ErrNotFound, "unknown media")
		return
	}
	data := sm.data()
	w.Header().Set("Content-Type", sm.meta.Mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
