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
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// events is the WebSocket stream of one session: first a hello with the head
// seq, then every event after ?since= (the head when absent), live. Events that
// this session caused are skipped (no echo).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	a, sess := s.acct(w, r)
	if a == nil {
		return
	}
	head := int64(len(a.events))
	s.mu.Unlock()
	since := head
	reason := ""
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeErr(w, 400, proto.ErrInvalidInput, "since must be a non-negative integer")
			return
		}
		since = v
		if since > head {
			since, reason = head, "cursor_ahead"
		}
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	st := &stream{token: sess.token, kick: make(chan int, 1)}
	s.mu.Lock()
	a.streams[st] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(a.streams, st)
		s.mu.Unlock()
	}()
	ctx := conn.CloseRead(context.WithoutCancel(r.Context()))
	write := func(v any) bool {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return wsjson.Write(wctx, conn, v) == nil
	}
	if !write(proto.Event{Seq: head, Type: proto.EventHello, Reason: reason, TS: time.Now().UTC()}) {
		return
	}
	for {
		s.mu.Lock()
		valid := s.sessions[sess.token] != nil && s.accounts[a.ID] == a
		var batch []proto.Event
		for _, rec := range a.events[min(since, int64(len(a.events))):] {
			since = rec.evt.Seq
			if rec.origin != sess.token {
				batch = append(batch, rec.evt)
			}
		}
		notify := a.notify
		s.mu.Unlock()
		if !valid {
			loggedOut(conn, write)
			return
		}
		for _, evt := range batch {
			if !write(evt) {
				return
			}
		}
		select {
		case <-notify:
		case code := <-st.kick:
			if code == proto.CloseLoggedOut {
				loggedOut(conn, write)
			} else {
				_ = conn.Close(websocket.StatusCode(code), "disconnected by the server")
			}
			return
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func loggedOut(conn *websocket.Conn, write func(any) bool) {
	write(proto.Event{Type: proto.EventLoggedOut, Reason: "session_revoked", TS: time.Now().UTC()})
	_ = conn.Close(websocket.StatusCode(proto.CloseLoggedOut), "logged out")
}
