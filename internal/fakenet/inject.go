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

package fakenet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
)

// Redeliverer re-appends the newest message_upsert frame of a login to its outbox.
type Redeliverer interface {
	Redeliver(ctx context.Context, loginID string) (int64, error)
}

// Routes returns the fake-only control endpoints (PROTOCOL.md, "Fake network").
func Routes(conn *Connector, outbox Redeliverer) map[string]http.Handler {
	h := &control{conn: conn, outbox: outbox}
	return map[string]http.Handler{
		"POST /fake/inject":      http.HandlerFunc(h.inject),
		"POST /fake/seed":        http.HandlerFunc(h.seed),
		"POST /fake/revoke":      http.HandlerFunc(h.revoke),
		"POST /fake/redeliver":   http.HandlerFunc(h.redeliver),
		"POST /fake/capture-url": http.HandlerFunc(h.captureURL),
	}
}

// InjectRequest drives the fake network from outside (POST /fake/inject).
type InjectRequest struct {
	LoginID        string `json:"loginId"`
	Kind           string `json:"kind"` // message | edit | delete | reaction | unreaction | read | state
	ConversationID string `json:"conversationId"`
	SenderID       string `json:"senderId"`
	MessageID      string `json:"messageId"`
	Text           string `json:"text"`
	Emoji          string `json:"emoji"`
	State          string `json:"state"`
	Attachment     *struct {
		FileName   string `json:"fileName"`
		MimeType   string `json:"mimeType"`
		DataBase64 string `json:"dataBase64"`
	} `json:"attachment"`
}

type control struct {
	conn   *Connector
	outbox Redeliverer
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (h *control) client(w http.ResponseWriter, loginKey string) *Client {
	loginID, _ := strings.CutPrefix(loginKey, h.conn.id+":")
	c := h.conn.Client(networkid.UserLoginID(loginID))
	if c == nil {
		fail(w, http.StatusNotFound, "account_not_found", "unknown fake login")
	}
	return c
}

func (h *control) inject(w http.ResponseWriter, r *http.Request) {
	var req InjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "invalid_input", "bad json")
		return
	}
	c := h.client(w, req.LoginID)
	if c == nil {
		return
	}
	if req.SenderID == "" {
		req.SenderID = "alice"
	}
	switch req.Kind {
	case "message":
		var file *File
		if a := req.Attachment; a != nil {
			data, err := base64.StdEncoding.DecodeString(a.DataBase64)
			if err != nil {
				fail(w, http.StatusBadRequest, "invalid_input", "attachment.dataBase64 is not base64")
				return
			}
			file = &File{FileName: a.FileName, MimeType: a.MimeType, Data: data}
		}
		c.IncomingAt(req.ConversationID, req.SenderID, req.MessageID, req.Text, file, time.Now())
	case "edit":
		c.Edit(req.ConversationID, req.SenderID, req.MessageID, req.Text)
	case "delete":
		c.Delete(req.ConversationID, req.SenderID, req.MessageID)
	case "reaction", "unreaction":
		c.React(req.ConversationID, req.SenderID, req.MessageID, req.Emoji, req.Kind == "unreaction")
	case "read":
		c.Read(req.ConversationID, req.SenderID, req.MessageID)
	case "state":
		c.SetState(status.BridgeStateEvent(req.State))
	default:
		fail(w, http.StatusBadRequest, "invalid_input", "unknown kind")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// captureURL sets the page of flow `web` for logins that start afterwards.
func (h *control) captureURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "invalid_input", "bad json")
		return
	}
	if req.URL != "" {
		u, err := url.Parse(req.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			fail(w, http.StatusBadRequest, "invalid_input", "url must be an http or https URL")
			return
		}
	}
	h.conn.SetCaptureURL(req.URL)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": h.conn.CaptureURL()})
}

type loginRequest struct {
	LoginID string `json:"loginId"`
}

func decodeLogin(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LoginID == "" {
		fail(w, http.StatusBadRequest, "invalid_input", "loginId is required")
		return "", false
	}
	return req.LoginID, true
}

func (h *control) revoke(w http.ResponseWriter, r *http.Request) {
	key, ok := decodeLogin(w, r)
	if !ok {
		return
	}
	if c := h.client(w, key); c != nil {
		c.Revoke()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *control) redeliver(w http.ResponseWriter, r *http.Request) {
	key, ok := decodeLogin(w, r)
	if !ok {
		return
	}
	seq, err := h.outbox.Redeliver(r.Context(), key)
	if err != nil {
		fail(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	} else if seq == 0 {
		fail(w, http.StatusNotFound, "message_not_found", "the login has no message to redeliver")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"seq": seq})
}

// SeedPNG is a 1x1 PNG, the attachment of the seeded media message.
var SeedPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

const (
	SeedGroup        = "team"
	SeedDM           = "dm-alice"
	SeedGroupSize    = 10
	SeedMediaMessage = "seed-team-05"
)

// seed creates two chats with past messages through the normal bridgev2 path and
// waits until they are bridged. Message ids are fixed, so a second seed is a no-op.
func (h *control) seed(w http.ResponseWriter, r *http.Request) {
	key, ok := decodeLogin(w, r)
	if !ok {
		return
	}
	c := h.client(w, key)
	if c == nil {
		return
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	var mu sync.Mutex
	attachmentID := ""
	deadline := time.Now().Add(10 * time.Second)
	// bridgev2 has unlocked writes while it creates a portal (portal room id, ghost info). The seed
	// therefore waits for the first message of a chat before it queues more, and for the group
	// before it starts the DM, which shares the ghost "alice". The race detector fails otherwise.
	waitBridged := func(ids ...string) bool {
		for _, id := range ids {
			for {
				msg, err := h.conn.Bridge.DB.Message.GetFirstPartByID(r.Context(), c.login.ID, networkid.MessageID(id))
				if err == nil && msg != nil {
					break
				}
				if time.Now().After(deadline) {
					fail(w, http.StatusGatewayTimeout, "internal_error", "seed messages were not bridged in time")
					return false
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		return true
	}
	for i := 1; i <= SeedGroupSize; i++ {
		id := fmt.Sprintf("seed-team-%02d", i)
		sender := []string{"alice", "bob"}[i%2]
		var file *File
		if id == SeedMediaMessage {
			file = &File{FileName: "pixel.png", MimeType: "image/png", Data: SeedPNG}
		}
		c.IncomingAt(SeedGroup, sender, id, fmt.Sprintf("team message %d", i), file, base.Add(time.Duration(i)*time.Minute), func(att string) {
			mu.Lock()
			attachmentID = att
			mu.Unlock()
		})
		if i == 1 && !waitBridged(id) {
			return
		}
	}
	if !waitBridged(fmt.Sprintf("seed-team-%02d", SeedGroupSize), SeedMediaMessage) {
		return
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("seed-dm-%02d", i)
		c.IncomingAt(SeedDM, "alice", id, fmt.Sprintf("dm message %d", i), nil, base.Add(time.Duration(i)*time.Minute+30*time.Second))
		if i == 1 && !waitBridged(id) {
			return
		}
	}
	if !waitBridged("seed-dm-03") {
		return
	}
	mu.Lock()
	att := attachmentID
	mu.Unlock()
	h.conn.mu.Lock()
	if att != "" {
		h.conn.seedMedia[c.login.ID] = att
	} else {
		att = h.conn.seedMedia[c.login.ID]
	}
	h.conn.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sendTarget": map[string]string{"conversationId": SeedGroup},
		"media":      map[string]string{"conversationId": SeedGroup, "messageId": SeedMediaMessage, "attachmentId": att},
	})
}
