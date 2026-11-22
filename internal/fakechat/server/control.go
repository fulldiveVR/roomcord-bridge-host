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
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/media"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

func (s *Server) controlRoutes(mux *http.ServeMux) {
	c := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.cfg.ControlToken == "" || !constantEq(bearer(r), s.cfg.ControlToken) {
				writeErr(w, 401, proto.ErrUnauthorized, "missing or invalid control token")
				return
			}
			h(w, r)
		}))
	}
	c("POST /control/reset", s.ctlReset)
	c("GET /control/accounts", s.ctlAccounts)
	c("POST /control/accounts", s.ctlCreateAccount)
	c("POST /control/accounts/{accountId}/chats", s.ctlCreateChat)
	c("GET /control/accounts/{accountId}/chats", s.ctlChats)
	c("POST /control/accounts/{accountId}/inject", s.ctlInject)
	c("POST /control/accounts/{accountId}/scenario", s.ctlScenario)
	c("GET /control/accounts/{accountId}/sent", s.ctlSent)
	c("POST /control/accounts/{accountId}/disconnect", s.ctlDisconnect)
	c("POST /control/accounts/{accountId}/revoke", s.ctlRevoke)
	c("GET /control/qr", s.ctlQRList)
	c("POST /control/qr/{qrId}/approve", s.ctlQRApprove)
	c("POST /control/faults", s.ctlAddFault)
	c("DELETE /control/faults", s.ctlClearFaults)
	c("GET /control/media", s.ctlMedia)
}

func (s *Server) ctlReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Seed *int64 `json:"seed"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	seed := s.cfg.Seed
	if req.Seed != nil {
		seed = *req.Seed
	}
	s.Reset(seed)
	w.WriteHeader(204)
}

func (s *Server) controlAccount(a *account) proto.ControlAccount {
	out := proto.ControlAccount{Account: a.Account, Password: a.password, TwoFactorCode: a.twoFactor, Streams: len(a.streams), HeadSeq: int64(len(a.events))}
	for _, sess := range s.sessions {
		if sess.accountID == a.ID {
			out.Sessions++
		}
	}
	return out
}

func (s *Server) ctlAccounts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := []proto.ControlAccount{}
	for _, a := range s.accounts {
		out = append(out, s.controlAccount(a))
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

func (s *Server) ctlCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req proto.CreateAccount
	if !decode(w, r, &req) {
		return
	}
	if req.Phone != "" && !phoneRe.MatchString(req.Phone) {
		writeErr(w, 400, proto.ErrInvalidPhone, "phone must be E.164, for example +15550100009")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.createAccountLocked(req)
	if err != nil {
		writeErr(w, 409, proto.ErrInvalidInput, err.Error())
		return
	}
	writeJSON(w, 200, s.controlAccount(a))
}

// ctlAcct returns the account with s.mu held, or writes 404.
func (s *Server) ctlAcct(w http.ResponseWriter, r *http.Request) *account {
	s.mu.Lock()
	a := s.accounts[r.PathValue("accountId")]
	if a == nil {
		s.mu.Unlock()
		writeErr(w, 404, proto.ErrNotFound, "unknown account")
	}
	return a
}

func (s *Server) ctlChats(w http.ResponseWriter, r *http.Request) {
	a := s.ctlAcct(w, r)
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

func (s *Server) ctlCreateChat(w http.ResponseWriter, r *http.Request) {
	var req proto.CreateChat
	if !decode(w, r, &req) {
		return
	}
	a := s.ctlAcct(w, r)
	if a == nil {
		return
	}
	defer s.mu.Unlock()
	if req.Kind != "dm" && req.Kind != "group" {
		writeErr(w, 400, proto.ErrInvalidInput, "kind must be dm or group")
		return
	}
	if len(req.Members) == 0 || (req.Kind == "dm" && len(req.Members) != 1) {
		writeErr(w, 400, proto.ErrInvalidInput, "a dm has one other member, a group at least one")
		return
	}
	for _, m := range req.Members {
		if _, ok := s.user(m); !ok || m == a.ID {
			writeErr(w, 400, proto.ErrInvalidInput, "unknown member "+m)
			return
		}
	}
	if req.ID == "" {
		req.ID = s.nextID("chat")
	}
	if a.chats[req.ID] != nil {
		writeErr(w, 409, proto.ErrInvalidInput, "chat exists")
		return
	}
	if req.Title == "" {
		u, _ := s.user(req.Members[0])
		req.Title = u.Name
	}
	c := &chat{Chat: proto.Chat{ID: req.ID, Kind: req.Kind, Title: req.Title, Members: append([]string{a.ID}, req.Members...), LastMessageAt: s.now()}, byID: map[string]*proto.Message{}}
	a.chats[c.ID] = c
	view := c.view()
	s.emit(a, proto.Event{Type: proto.EventChatUpdate, ChatID: c.ID, Chat: &view}, "")
	writeJSON(w, 200, view)
}

func (s *Server) ctlInject(w http.ResponseWriter, r *http.Request) {
	var req proto.Inject
	if !decode(w, r, &req) {
		return
	}
	a := s.ctlAcct(w, r)
	if a == nil {
		return
	}
	res, err := s.injectLocked(a, req)
	s.mu.Unlock()
	if err != nil {
		writeErr(w, 400, proto.ErrInvalidInput, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// injectLocked applies one remote action. It runs with s.mu held.
func (s *Server) injectLocked(a *account, req proto.Inject) (proto.InjectResult, error) {
	c := a.chats[req.ChatID]
	if c == nil {
		return proto.InjectResult{}, fmt.Errorf("unknown chat %q", req.ChatID)
	}
	if req.From == "" {
		for _, m := range c.Members {
			if m != a.ID {
				req.From = m
				break
			}
		}
	}
	if !c.isMember(req.From) && req.Type != proto.InjectMemberJoin {
		return proto.InjectResult{}, fmt.Errorf("%q is not a member of %s", req.From, c.ID)
	}
	target := func() (*proto.Message, error) {
		m := c.byID[req.MessageID]
		if m == nil {
			return nil, fmt.Errorf("unknown message %q", req.MessageID)
		}
		return m, nil
	}
	ts := s.now()
	evt := proto.Event{ChatID: c.ID, UserID: req.From, TS: ts}
	var msg *proto.Message
	switch req.Type {
	case "", proto.InjectMessage:
		m := &proto.Message{ID: s.nextID("msg"), ChatID: c.ID, SenderID: req.From, TS: ts, Kind: proto.KindText,
			Text: req.Text, Entities: req.Entities, ReplyTo: req.ReplyTo}
		if req.ReplyTo != "" && c.byID[req.ReplyTo] == nil {
			return proto.InjectResult{}, fmt.Errorf("unknown replyTo %q", req.ReplyTo)
		}
		switch {
		case req.Media != "":
			e, ok := media.Get(req.Media)
			if !ok {
				return proto.InjectResult{}, fmt.Errorf("unknown media %q, see GET /control/media", req.Media)
			}
			meta := e.Meta(catalogID(e.Name))
			if req.Kind != "" {
				meta.Kind = req.Kind
			}
			m.Kind, m.Media = meta.Kind, &meta
		case req.Location != nil:
			m.Kind, m.Location = proto.KindLocation, req.Location
		case req.Contact != nil:
			m.Kind, m.Contact = proto.KindContact, req.Contact
		case req.Text == "":
			return proto.InjectResult{}, fmt.Errorf("a message needs text, media, location or contact")
		}
		c.append(m)
		msg = m
		evt.Type = proto.EventMessage
	case proto.InjectEdit:
		m, err := target()
		if err != nil {
			return proto.InjectResult{}, err
		}
		if m.SenderID != req.From {
			return proto.InjectResult{}, fmt.Errorf("only the sender %q can edit %s", m.SenderID, m.ID)
		}
		m.Text, m.Entities, m.EditedAt = req.Text, req.Entities, &ts
		msg = m
		evt.Type, evt.MessageID = proto.EventMessageEdit, m.ID
	case proto.InjectDelete:
		m, err := target()
		if err != nil {
			return proto.InjectResult{}, err
		}
		deleteContent(m)
		evt.Type, evt.MessageID = proto.EventMessageDelete, m.ID
	case proto.InjectReactionAdd, proto.InjectReactionRemove:
		m, err := target()
		if err != nil {
			return proto.InjectResult{}, err
		}
		if req.Emoji == "" {
			return proto.InjectResult{}, fmt.Errorf("emoji is required")
		}
		add := req.Type == proto.InjectReactionAdd
		setReaction(m, req.From, req.Emoji, add, ts)
		evt.Type, evt.MessageID, evt.Emoji = proto.EventReactionAdd, m.ID, req.Emoji
		if !add {
			evt.Type = proto.EventReactionRemove
		}
	case proto.InjectRead:
		if req.MessageID == "" {
			req.MessageID = c.LastMessageID
		}
		if _, err := target(); err != nil {
			return proto.InjectResult{}, err
		}
		evt.Type, evt.MessageID = proto.EventRead, req.MessageID
	case proto.InjectTyping:
		evt.Type, evt.Typing = proto.EventTyping, req.Typing == nil || *req.Typing
	case proto.InjectMemberJoin, proto.InjectMemberLeave, proto.InjectRename:
		if c.Kind != "group" {
			return proto.InjectResult{}, fmt.Errorf("%s needs a group chat", req.Type)
		}
		sys := &proto.System{}
		switch req.Type {
		case proto.InjectMemberJoin:
			if _, ok := s.user(req.UserID); !ok || c.isMember(req.UserID) {
				return proto.InjectResult{}, fmt.Errorf("userId %q is unknown or already a member", req.UserID)
			}
			c.Members = append(c.Members, req.UserID)
			sys.Action, sys.UserID = proto.SystemMemberJoined, req.UserID
		case proto.InjectMemberLeave:
			if !c.isMember(req.UserID) || req.UserID == a.ID {
				return proto.InjectResult{}, fmt.Errorf("userId %q is not another member", req.UserID)
			}
			for i, m := range c.Members {
				if m == req.UserID {
					c.Members = append(c.Members[:i], c.Members[i+1:]...)
					break
				}
			}
			sys.Action, sys.UserID = proto.SystemMemberLeft, req.UserID
		default:
			if strings.TrimSpace(req.Title) == "" {
				return proto.InjectResult{}, fmt.Errorf("title is required")
			}
			c.Title = req.Title
			sys.Action, sys.Title = proto.SystemRenamed, req.Title
		}
		m := &proto.Message{ID: s.nextID("msg"), ChatID: c.ID, SenderID: req.From, TS: ts, Kind: proto.KindSystem, System: sys}
		c.append(m)
		msg = m
		view := c.view()
		evt.Type, evt.Chat = proto.EventChatUpdate, &view
	default:
		return proto.InjectResult{}, fmt.Errorf("unknown type %q", req.Type)
	}
	var out *proto.Message
	if msg != nil {
		cp := copyMessage(msg)
		out = &cp
		evt.Message = out
	}
	seq := s.emit(a, evt, "")
	return proto.InjectResult{Seq: seq, Message: out}, nil
}

// Scenario names.
const (
	ScenarioAllTypes    = "all-types"
	ScenarioMedia       = "media"
	ScenarioOversize    = "oversize"
	ScenarioGroupEvents = "group-events"
	ScenarioBurst       = "burst"
)

var longText = strings.Repeat("This is a long FakeChat message that tests wrapping, truncation and storage of large text bodies. ", 45)

func (s *Server) ctlScenario(w http.ResponseWriter, r *http.Request) {
	var req proto.ScenarioRequest
	if !decode(w, r, &req) {
		return
	}
	a := s.ctlAcct(w, r)
	if a == nil {
		return
	}
	res, err := s.scenarioLocked(a, req)
	s.mu.Unlock()
	if err != nil {
		writeErr(w, 400, proto.ErrInvalidInput, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) scenarioLocked(a *account, req proto.ScenarioRequest) (proto.ScenarioResult, error) {
	if req.ChatID == "" {
		req.ChatID = ChatProject
	}
	c := a.chats[req.ChatID]
	if c == nil {
		return proto.ScenarioResult{}, fmt.Errorf("unknown chat %q", req.ChatID)
	}
	res := proto.ScenarioResult{ChatID: c.ID}
	others := []string{}
	for _, m := range c.Members {
		if m != a.ID {
			others = append(others, m)
		}
	}
	from := others[0]
	second := others[len(others)-1]
	ids := map[string]string{}
	step := func(name string, in proto.Inject) error {
		in.ChatID = c.ID
		if in.From == "" {
			in.From = from
		}
		out, err := s.injectLocked(a, in)
		if err != nil {
			return fmt.Errorf("step %s: %w", name, err)
		}
		st := proto.ScenarioStep{Name: name, Type: in.Type, Seq: out.Seq}
		if st.Type == "" {
			st.Type = proto.InjectMessage
		}
		if out.Message != nil {
			st.MessageID = out.Message.ID
		} else {
			st.MessageID = in.MessageID
		}
		ids[name] = st.MessageID
		res.Steps = append(res.Steps, st)
		return nil
	}
	mediaSteps := []struct{ name, file, kind string }{
		{"image_jpg", media.PhotoJPG, ""},
		{"image_png", media.PhotoPNG, ""},
		{"image_gif", media.AnimGIF, ""},
		{"image_webp", media.PhotoWebP, ""},
		{"video", media.ClipMP4, ""},
		{"voice", media.VoiceOGG, ""},
		{"audio", media.ToneWAV, ""},
		{"file_pdf", media.DocPDF, ""},
		{"file_zip", media.ArchiveZIP, ""},
		{"sticker", media.StickerWebP, ""},
	}
	oversizeSteps := []struct{ name, file, kind string }{
		{"file_at_limit", media.LimitBin, ""},
		{"file_oversize", media.OversizeBin, ""},
		{"image_oversize", media.OversizePhoto, ""},
	}
	runMedia := func(list []struct{ name, file, kind string }) error {
		for _, m := range list {
			if err := step(m.name, proto.Inject{Media: m.file, Kind: m.kind, Text: captionFor(m.name)}); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	switch req.Name {
	case ScenarioAllTypes:
		err = firstErr(
			func() error { return step("text", proto.Inject{Text: "Hello from FakeChat"}) },
			func() error { return step("long_text", proto.Inject{Text: longText}) },
			func() error {
				return step("formatted", proto.Inject{Text: "bold italic code link strike", Entities: []proto.Entity{
					{Type: "bold", Offset: 0, Length: 4}, {Type: "italic", Offset: 5, Length: 6}, {Type: "code", Offset: 12, Length: 4},
					{Type: "link", Offset: 17, Length: 4, URL: "https://example.com/fakechat"}, {Type: "strike", Offset: 22, Length: 6},
				}})
			},
			func() error { return step("emoji_only", proto.Inject{Text: "🎉👍😀"}) },
			func() error {
				return step("reply", proto.Inject{From: second, Text: "This is a reply", ReplyTo: ids["text"]})
			},
			func() error { return runMedia(mediaSteps) },
			func() error {
				return step("location", proto.Inject{Location: &proto.Location{Lat: 48.8584, Lon: 2.2945, Name: "Eiffel Tower"}})
			},
			func() error {
				return step("contact", proto.Inject{Contact: &proto.Contact{Name: "Frank Fake", Phone: "+15550200099"}})
			},
			func() error { return runMedia(oversizeSteps) },
			func() error { return step("own_message", proto.Inject{From: a.ID, Text: "Sent from my phone"}) },
			func() error {
				return step("edit", proto.Inject{Type: proto.InjectEdit, MessageID: ids["text"], Text: "Hello from FakeChat (edited)"})
			},
			func() error {
				return step("reaction_add", proto.Inject{Type: proto.InjectReactionAdd, MessageID: ids["image_jpg"], Emoji: "❤️"})
			},
			func() error {
				return step("reaction_add_2", proto.Inject{Type: proto.InjectReactionAdd, From: second, MessageID: ids["image_jpg"], Emoji: "👍"})
			},
			func() error {
				return step("reaction_remove", proto.Inject{Type: proto.InjectReactionRemove, From: second, MessageID: ids["image_jpg"], Emoji: "👍"})
			},
			func() error { return step("to_delete", proto.Inject{Text: "This message will be deleted"}) },
			func() error {
				return step("delete", proto.Inject{Type: proto.InjectDelete, MessageID: ids["to_delete"]})
			},
			func() error { return step("typing_start", proto.Inject{Type: proto.InjectTyping, Typing: ptrTo(true)}) },
			func() error { return step("typing_stop", proto.Inject{Type: proto.InjectTyping, Typing: ptrTo(false)}) },
			func() error { return step("read", proto.Inject{Type: proto.InjectRead, MessageID: ids["own_message"]}) },
		)
		if err == nil && c.Kind == "group" {
			err = s.groupEvents(a, c, step)
		}
	case ScenarioMedia:
		err = runMedia(mediaSteps)
	case ScenarioOversize:
		err = runMedia(oversizeSteps)
	case ScenarioGroupEvents:
		if c.Kind != "group" {
			return res, fmt.Errorf("%s needs a group chat", req.Name)
		}
		err = s.groupEvents(a, c, step)
	case ScenarioBurst:
		n := req.Count
		if n <= 0 {
			n = 20
		}
		for i := 1; i <= n && err == nil; i++ {
			err = step(fmt.Sprintf("burst_%d", i), proto.Inject{From: others[i%len(others)], Text: fmt.Sprintf("Burst message %d", i)})
		}
	default:
		return res, fmt.Errorf("unknown scenario %q (all-types, media, oversize, group-events, burst)", req.Name)
	}
	return res, err
}

func (s *Server) groupEvents(a *account, c *chat, step func(string, proto.Inject) error) error {
	joiner := ""
	for _, ct := range contacts {
		if !c.isMember(ct.id) {
			joiner = ct.id
			break
		}
	}
	if joiner == "" {
		return fmt.Errorf("every contact is already in %s", c.ID)
	}
	oldTitle := c.Title
	return firstErr(
		func() error {
			return step("member_join", proto.Inject{Type: proto.InjectMemberJoin, UserID: joiner, From: joiner})
		},
		func() error {
			return step("member_leave", proto.Inject{Type: proto.InjectMemberLeave, UserID: joiner, From: joiner})
		},
		func() error {
			return step("rename", proto.Inject{Type: proto.InjectRename, Title: oldTitle + " (renamed)"})
		},
	)
}

func captionFor(name string) string {
	switch name {
	case "image_jpg":
		return "A JPEG photo"
	case "file_oversize", "image_oversize":
		return "Over the 8 MB relay limit"
	case "file_at_limit":
		return "Exactly 8 MiB"
	}
	return ""
}

func ptrTo[T any](v T) *T { return &v }

func firstErr(fns ...func() error) error {
	for _, f := range fns {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) ctlSent(w http.ResponseWriter, r *http.Request) {
	a := s.ctlAcct(w, r)
	if a == nil {
		return
	}
	out := append([]proto.SentRecord{}, a.sent...)
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) ctlDisconnect(w http.ResponseWriter, r *http.Request) {
	a := s.ctlAcct(w, r)
	if a == nil {
		return
	}
	var streams []*stream
	for st := range a.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	for _, st := range streams {
		kick(st, int(websocket.StatusGoingAway))
	}
	writeJSON(w, 200, map[string]int{"closed": len(streams)})
}

func (s *Server) ctlRevoke(w http.ResponseWriter, r *http.Request) {
	a := s.ctlAcct(w, r)
	if a == nil {
		return
	}
	streams := s.revokeLocked(a)
	s.mu.Unlock()
	for _, st := range streams {
		kick(st, proto.CloseLoggedOut)
	}
	w.WriteHeader(204)
}

func (s *Server) pendingQRs() []proto.PendingQR {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []proto.PendingQR{}
	for _, q := range s.qrs {
		if q.approvedBy == "" && time.Now().Before(q.ExpiresAt) {
			out = append(out, q.PendingQR)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QRID < out[j].QRID })
	return out
}

func (s *Server) ctlQRList(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.pendingQRs()) }

func (s *Server) ctlQRApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"accountId"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if req.AccountID == "" {
		req.AccountID = DemoAccount
	}
	if err := s.ApproveQR(r.PathValue("qrId"), req.AccountID); err != nil {
		writeErr(w, 404, proto.ErrNotFound, err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) ctlAddFault(w http.ResponseWriter, r *http.Request) {
	var req proto.Fault
	if !decode(w, r, &req) {
		return
	}
	if req.Kind != proto.FaultRateLimit && req.Kind != proto.FaultServerError {
		writeErr(w, 400, proto.ErrInvalidInput, "kind must be rate_limit or server_error")
		return
	}
	switch req.Scope {
	case "":
		req.Scope = proto.ScopeAny
	case proto.ScopeAny, proto.ScopeAuth, proto.ScopeSend, proto.ScopeHistory, proto.ScopeMedia, proto.ScopeEvents:
	default:
		writeErr(w, 400, proto.ErrInvalidInput, "scope must be any, auth, send, history, media or events")
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	s.mu.Lock()
	s.faults = append(s.faults, &fault{Fault: req})
	s.mu.Unlock()
	writeJSON(w, 200, req)
}

func (s *Server) ctlClearFaults(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.faults = nil
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *Server) ctlMedia(w http.ResponseWriter, r *http.Request) {
	out := []proto.CatalogEntry{}
	for _, name := range media.Names() {
		e, _ := media.Get(name)
		out = append(out, proto.CatalogEntry{Name: name, Kind: e.Kind, Mime: e.Mime, Size: int64(len(e.Data())), MediaID: catalogID(name),
			Width: e.Width, Height: e.Height, DurationMs: e.DurationMs})
	}
	writeJSON(w, 200, out)
}
