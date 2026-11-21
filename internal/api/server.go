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

// Package api serves the bridge-host wire protocol (PROTOCOL.md) over HTTP and WebSocket.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

type Server struct {
	Reg     *hosts.Registry
	Logins  *login.Engine
	Token   string
	Version string
	Log     zerolog.Logger
	// Extra routes (e.g. the fake network's inject endpoint), mounted behind auth.
	Extra map[string]http.Handler

	subsMu sync.Mutex
	subs   map[string]*subscriber

	sendsMu sync.Mutex
	sends   map[string]*inflightSend
}

func (s *Server) Handler() http.Handler {
	s.subs = make(map[string]*subscriber)
	s.sends = make(map[string]*inflightSend)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /networks", s.networks)
	mux.HandleFunc("POST /logins/start", s.loginStart)
	mux.HandleFunc("GET /logins/{processId}", s.loginGet)
	mux.HandleFunc("POST /logins/{processId}/submit", s.loginSubmit)
	mux.HandleFunc("DELETE /logins/{processId}", s.loginCancel)
	mux.HandleFunc("GET /accounts", s.accounts)
	mux.HandleFunc("GET /accounts/{loginId}/events", s.events)
	mux.HandleFunc("GET /accounts/{loginId}/conversations", s.conversations)
	mux.HandleFunc("POST /accounts/{loginId}/history", s.history)
	mux.HandleFunc("POST /accounts/{loginId}/send", s.send)
	mux.HandleFunc("POST /accounts/{loginId}/read", s.read)
	mux.HandleFunc("POST /accounts/{loginId}/media", s.uploadMedia)
	mux.HandleFunc("GET /accounts/{loginId}/media/{messageId}/{attachmentId}", s.downloadMedia)
	mux.HandleFunc("GET /accounts/{loginId}/health", s.accountHealth)
	mux.HandleFunc("POST /accounts/{loginId}/logout", s.logout)
	mux.HandleFunc("DELETE /accounts/{loginId}/data", s.deleteData)
	for pattern, h := range s.Extra {
		mux.Handle(pattern, h)
	}
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	expected := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if s.Token == "" || subtle.ConstantTimeCompare(got, expected) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r.WithContext(s.Log.WithContext(r.Context())))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var le *login.Error
	if errors.As(err, &le) {
		writeError(w, le.Status, le.Code, le.Message)
		return
	}
	zerolog.Ctx(r.Context()).Err(err).Str("path", r.URL.Path).Msg("Request failed")
	writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
}

func decode(r *http.Request, into any) error {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		return &login.Error{Status: 400, Code: "invalid_input", Message: "invalid JSON body: " + err.Error()}
	}
	return nil
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "version": s.Version, "networks": s.Reg.IDs()})
}

func (s *Server) networks(w http.ResponseWriter, r *http.Request) {
	out := []wire.Network{}
	for _, id := range s.Reg.IDs() {
		n := s.Reg.Get(id)
		flows := []wire.Flow{}
		for _, f := range n.Connector.GetLoginFlows() {
			flows = append(flows, wire.Flow{ID: f.ID, Name: f.Name, Description: f.Description})
		}
		gc := n.Connector.GetCapabilities()
		caps := map[string]any{
			"disappearingMessages": gc.DisappearingMessages,
			"implicitReadReceipts": gc.ImplicitReadReceipts,
			"loginReady":           n.ConfigError == nil,
		}
		if n.ConfigError != nil {
			caps["configError"] = n.ConfigError.Error()
		}
		wn := wire.Network{ID: id, DisplayName: n.Connector.GetName().DisplayName, Available: n.ConfigError == nil, Flows: flows, Capabilities: caps}
		if n.ConfigError != nil {
			wn.Reason = n.ConfigError.Error()
		}
		out = append(out, wn)
	}
	unavailable := s.Reg.Unavailable()
	ids := make([]string, 0, len(unavailable))
	for id := range unavailable {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, wire.Network{ID: id, DisplayName: id, Available: false, Reason: unavailable[id], Flows: []wire.Flow{},
			Capabilities: map[string]any{"loginReady": false, "configError": unavailable[id]}})
	}
	writeJSON(w, 200, out)
}

func (s *Server) loginStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Network          string `json:"network"`
		UserID           string `json:"userId"`
		FlowID           string `json:"flowId"`
		ReconnectLoginID string `json:"reconnectLoginId"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	p, step, err := s.Logins.Start(r.Context(), req.Network, req.UserID, req.FlowID, req.ReconnectLoginID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, wire.LoginResponse{ProcessID: p.ID, Step: step})
}

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("processId")
	step, err := s.Logins.Get(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, wire.LoginResponse{ProcessID: id, Step: step})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StepID  string            `json:"stepId"`
		Fields  map[string]string `json:"fields"`
		Cookies map[string]string `json:"cookies"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("processId")
	step, err := s.Logins.Submit(r.Context(), id, req.StepID, req.Fields, req.Cookies)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, wire.LoginResponse{ProcessID: id, Step: step})
}

func (s *Server) loginCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Logins.Cancel(r.PathValue("processId")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pageLimit(raw string, def, max int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return min(n, max)
}

func isNormalClose(err error) bool {
	if err == nil {
		return true
	}
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || errors.Is(err, context.Canceled)
}

func cleanID(s string) string { return strings.TrimSpace(s) }
