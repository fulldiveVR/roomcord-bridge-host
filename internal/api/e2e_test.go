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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

const token = "test-token"

type harness struct {
	t    *testing.T
	ts   *httptest.Server
	reg  *hosts.Registry
	fake *fakenet.Connector
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := fakenet.New()
	log := zerolog.Nop()
	reg, err := hosts.Start(context.Background(), t.TempDir(), []hosts.Factory{
		{ID: fakenet.NetworkID, New: func() (bridgev2.NetworkConnector, error) { return fake, nil }},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Reg: reg, Logins: login.NewEngine(reg, log), Token: token, Version: "test", Log: log,
		Extra: fakenet.Routes(fake, reg.Store), Public: fakenet.ProviderRoutes()}
	ts := httptest.NewServer(srv.Handler())
	fake.SetCaptureURL(ts.URL + "/fake-provider/login")
	t.Cleanup(func() {
		ts.Close()
		reg.Stop()
	})
	return &harness{t: t, ts: ts, reg: reg, fake: fake}
}

func (h *harness) do(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (h *harness) doJSON(method, path string, body, into any, wantStatus int) {
	h.t.Helper()
	code, raw := h.do(method, path, body)
	if code != wantStatus {
		h.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, wantStatus, raw)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			h.t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

func (h *harness) login(userID, phone string) (string, *fakenet.Client) {
	h.t.Helper()
	var r wire.LoginResponse
	h.doJSON("POST", "/logins/start", map[string]any{"network": "fake", "userId": userID, "flowId": "phone"}, &r, 200)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": map[string]string{"phone_number": phone}}, &r, 200)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": map[string]string{"code": fakenet.MagicCode}}, &r, 200)
	if r.Step.Kind != wire.KindDone {
		h.t.Fatalf("login not done: %+v", r.Step)
	}
	c := h.fake.Client(networkid.UserLoginID(r.Step.RemoteID))
	if c == nil {
		h.t.Fatal("no fake client after login")
	}
	return r.Step.LoginID, c
}

type frame struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func (h *harness) dial(loginKey string, cursor *int64) *websocket.Conn {
	h.t.Helper()
	u := "ws" + strings.TrimPrefix(h.ts.URL, "http") + "/accounts/" + loginKey + "/events"
	if cursor != nil {
		u += fmt.Sprintf("?cursor=%d", *cursor)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func next(t *testing.T, conn *websocket.Conn) frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var f frame
	if err := wsjson.Read(ctx, conn, &f); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

// until reads frames until one of type typ arrives, returning it and every frame read.
func until(t *testing.T, conn *websocket.Conn, typ string) (frame, []frame) {
	t.Helper()
	var seen []frame
	for range 50 {
		f := next(t, conn)
		seen = append(seen, f)
		if f.Type == typ {
			return f, seen
		}
	}
	t.Fatalf("no %s frame in %d frames", typ, len(seen))
	return frame{}, nil
}

func decodeP[T any](t *testing.T, f frame) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(f.Payload, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAuthRequired(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	var health map[string]any
	h.doJSON("GET", "/health", nil, &health, 200)
	if health["ok"] != true {
		t.Fatalf("health %v", health)
	}
}

func TestLoginFlow(t *testing.T) {
	h := newHarness(t)
	var nets []wire.Network
	h.doJSON("GET", "/networks", nil, &nets, 200)
	if len(nets) != 1 || nets[0].ID != "fake" || nets[0].Flows[0].ID != "phone" {
		t.Fatalf("networks %+v", nets)
	}
	var r wire.LoginResponse
	h.doJSON("POST", "/logins/start", map[string]any{"network": "fake", "userId": "u1", "flowId": "phone"}, &r, 200)
	if r.Step.Kind != wire.KindPhoneNumberInput || r.Step.FieldID != "phone_number" {
		t.Fatalf("first step %+v", r.Step)
	}
	pid := r.ProcessID
	h.doJSON("POST", "/logins/"+pid+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": map[string]string{"phone_number": "not a phone"}}, &r, 200)
	if r.Step.Kind != wire.KindPhoneNumberInput || r.Step.Error == nil || r.Step.Error.Code != "invalid_phone_number" {
		t.Fatalf("validation step %+v", r.Step)
	}
	h.doJSON("POST", "/logins/"+pid+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": map[string]string{"phone_number": "+15550001111"}}, &r, 200)
	if r.Step.Kind != wire.KindCodeInput {
		t.Fatalf("code step %+v", r.Step)
	}
	codeStepID := r.Step.StepID
	h.doJSON("POST", "/logins/"+pid+"/submit", map[string]any{"stepId": "stale", "fields": map[string]string{"code": "1"}}, nil, 409)
	h.doJSON("POST", "/logins/"+pid+"/submit", map[string]any{"stepId": codeStepID, "fields": map[string]string{"code": "000000"}}, &r, 200)
	if r.Step.Kind != wire.KindCodeInput || r.Step.StepID != codeStepID || r.Step.Error == nil || r.Step.Error.Code != "invalid_code" {
		t.Fatalf("retry step %+v", r.Step)
	}
	h.doJSON("POST", "/logins/"+pid+"/submit", map[string]any{"stepId": codeStepID, "fields": map[string]string{"code": "111111"}}, &r, 200)
	if r.Step.Kind != wire.KindDone || r.Step.LoginID != "fake:15550001111" || r.Step.RemoteName != "+15550001111" {
		t.Fatalf("done step %+v", r.Step)
	}
	h.doJSON("GET", "/logins/"+pid, nil, &r, 200)
	if r.Step.Kind != wire.KindDone {
		t.Fatalf("GET after done %+v", r.Step)
	}
	var accounts []wire.Account
	h.doJSON("GET", "/accounts?userId=u1", nil, &accounts, 200)
	if len(accounts) != 1 || accounts[0].LoginID != "fake:15550001111" || accounts[0].State != "connected" {
		t.Fatalf("accounts %+v", accounts)
	}
	var health wire.Health
	h.doJSON("GET", "/accounts/fake:15550001111/health", nil, &health, 200)
	if health.State != "connected" {
		t.Fatalf("health %+v", health)
	}

	h.doJSON("POST", "/logins/start", map[string]any{"network": "fake", "userId": "u1", "flowId": "phone"}, &r, 200)
	h.doJSON("DELETE", "/logins/"+r.ProcessID, nil, nil, 204)
	h.doJSON("GET", "/logins/"+r.ProcessID, nil, nil, 404)
}

func TestIncomingEventsAndCursorReplay(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	cp := next(t, conn)
	if cp.Type != wire.EventCheckpoint {
		t.Fatalf("first frame %+v, want checkpoint", cp)
	}

	client.Incoming("dm-alice", "alice", "m1", "hello")
	f1, seen := until(t, conn, wire.EventMessageUpsert)
	if seen[0].Type != wire.EventConversationUpsert {
		t.Fatalf("expected conversation_upsert before the message, got %s", seen[0].Type)
	}
	conv := decodeP[wire.ConversationUpsertPayload](t, seen[0])
	if conv.Conversation.ExternalConversationID != "dm-alice" || conv.Conversation.Kind != "dm" {
		t.Fatalf("conversation %+v", conv)
	}
	msg := decodeP[wire.MessageUpsertPayload](t, f1).Message
	if msg.ExternalMessageID != "m1" || msg.Text != "hello" || msg.Direction != "inbound" || msg.SenderExternalID != "alice" || f1.Seq <= cp.Seq {
		t.Fatalf("message %+v seq %d (checkpoint %d)", msg, f1.Seq, cp.Seq)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	client.Incoming("dm-alice", "alice", "m2", "second")
	client.Incoming("dm-alice", "alice", "m3", "third")
	waitHistory(t, h, key, "dm-alice", 3)

	cursor := f1.Seq
	conn2 := h.dial(key, &cursor)
	var texts []string
	prev := cursor
	for len(texts) < 2 {
		f := next(t, conn2)
		if f.Seq <= prev {
			t.Fatalf("seq not increasing: %d after %d", f.Seq, prev)
		}
		prev = f.Seq
		if f.Type == wire.EventMessageUpsert {
			texts = append(texts, decodeP[wire.MessageUpsertPayload](t, f).Message.Text)
		}
	}
	if texts[0] != "second" || texts[1] != "third" {
		t.Fatalf("replayed %v", texts)
	}

	zero := int64(0)
	conn3 := h.dial(key, &zero)
	first := next(t, conn3)
	if first.Seq != 1 {
		t.Fatalf("cursor 0 replay starts at %d", first.Seq)
	}
	_ = conn2
}

func waitHistory(t *testing.T, h *harness, key, conv string, n int) []wire.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var page wire.Page[wire.Message]
		code, raw := h.do("POST", "/accounts/"+key+"/history", map[string]any{"conversationId": conv, "limit": 50})
		if code == 200 {
			_ = json.Unmarshal(raw, &page)
			if len(page.Items) >= n {
				return page.Items
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("history has %d messages, want %d (last status %d: %s)", len(page.Items), n, code, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEditDeleteReactionReceipt(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	next(t, conn)

	client.Incoming("grp", "alice", "m1", "hello")
	until(t, conn, wire.EventMessageUpsert)

	client.Edit("grp", "alice", "m1", "hello edited")
	f, _ := until(t, conn, wire.EventMessageUpsert)
	edited := decodeP[wire.MessageUpsertPayload](t, f).Message
	if edited.ExternalMessageID != "m1" || edited.Text != "hello edited" || edited.EditedAt == nil {
		t.Fatalf("edit %+v", edited)
	}

	client.React("grp", "bob", "m1", "👍", false)
	f, _ = until(t, conn, wire.EventReactionUpsert)
	react := decodeP[wire.ReactionPayload](t, f)
	if react.ExternalMessageID != "m1" || strings.TrimSuffix(react.Reaction.Emoji, "\ufe0f") != "👍" || react.Reaction.SenderExternalID != "bob" {
		t.Fatalf("reaction %+v", react)
	}
	client.React("grp", "bob", "m1", "👍", true)
	f, _ = until(t, conn, wire.EventReactionDelete)
	if r := decodeP[wire.ReactionPayload](t, f); r.ExternalMessageID != "m1" || strings.TrimSuffix(r.Reaction.Emoji, "\ufe0f") != "👍" {
		t.Fatalf("reaction delete %+v", r)
	}

	client.Read("grp", "bob", "m1")
	f, _ = until(t, conn, wire.EventReadReceipt)
	if rr := decodeP[wire.ReadReceiptPayload](t, f); rr.LastReadExternalMessageID != "m1" || rr.ReaderExternalID != "bob" || rr.IsSelf {
		t.Fatalf("receipt %+v", rr)
	}

	client.Delete("grp", "alice", "m1")
	f, _ = until(t, conn, wire.EventMessageDelete)
	if d := decodeP[wire.MessageDeletePayload](t, f); d.ExternalMessageID != "m1" || d.ExternalConversationID != "grp" {
		t.Fatalf("delete %+v", d)
	}
	items := waitHistory(t, h, key, "grp", 1)
	if items[0].DeletedAt == nil || items[0].Text != "hello edited" {
		t.Fatalf("history after delete %+v", items[0])
	}

	client.SetState(status.StateBadCredentials)
	f, _ = until(t, conn, wire.EventAccountStatus)
	if st := decodeP[wire.AccountStatusPayload](t, f); st.Status != "needs_reauth" || st.Code == nil || *st.Code != "session_revoked" {
		t.Fatalf("status %+v", st)
	}
}

func TestSendIsIdempotent(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	next(t, conn)
	client.Incoming("dm-alice", "alice", "m1", "hello")
	until(t, conn, wire.EventMessageUpsert)

	body := map[string]any{"conversationId": "dm-alice", "clientMessageId": "cm-1", "text": "hi back", "replyToMessageId": "m1"}
	results := make([]wire.SendResponse, 3)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { h.doJSON("POST", "/accounts/"+key+"/send", body, &results[i], 200) })
	}
	wg.Wait()
	h.doJSON("POST", "/accounts/"+key+"/send", body, &results[2], 200)
	for _, r := range results[1:] {
		if r.ExternalMessageID != results[0].ExternalMessageID || !r.SentAt.Equal(results[0].SentAt) {
			t.Fatalf("different results %+v", results)
		}
	}
	sent := h.fake.Sent()
	if len(sent) != 1 || sent[0].Text != "hi back" || sent[0].ReplyTo != "m1" {
		t.Fatalf("network received %+v", sent)
	}
	f, _ := until(t, conn, wire.EventMessageUpsert)
	out := decodeP[wire.MessageUpsertPayload](t, f).Message
	if out.Direction != "outbound" || out.ExternalMessageID != results[0].ExternalMessageID || out.ReplyToExternalMessageID == nil || *out.ReplyToExternalMessageID != "m1" {
		t.Fatalf("outbound event %+v", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var extra frame
	if err := wsjson.Read(ctx, conn, &extra); err == nil && extra.Type == wire.EventMessageUpsert {
		t.Fatalf("second outbound message event: %+v", extra)
	}

	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "cm-2", "text": "again"}, &results[0], 200)
	if len(h.fake.Sent()) != 2 {
		t.Fatalf("a new clientMessageId must send again")
	}
	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "nope", "clientMessageId": "cm-3", "text": "x"}, nil, 404)
	h.doJSON("POST", "/accounts/"+key+"/read", map[string]any{"conversationId": "dm-alice"}, nil, 204)

	var convs wire.Page[wire.Conversation]
	h.doJSON("GET", "/accounts/"+key+"/conversations", nil, &convs, 200)
	if len(convs.Items) != 1 || convs.Items[0].ExternalConversationID != "dm-alice" {
		t.Fatalf("conversations %+v", convs)
	}
}

func TestLogoutAndDataDeletion(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	next(t, conn)
	client.Incoming("dm-alice", "alice", "m1", "hello")
	until(t, conn, wire.EventMessageUpsert)

	h.doJSON("POST", "/accounts/"+key+"/logout", nil, nil, 204)
	f, _ := until(t, conn, wire.EventAccountStatus)
	if st := decodeP[wire.AccountStatusPayload](t, f); st.Status != "needs_reauth" {
		t.Fatalf("status after logout %+v", st)
	}
	if lo := h.fake.LoggedOut(); len(lo) != 1 || lo[0] != "15550001111" {
		t.Fatalf("remote logout not called: %v", lo)
	}
	var accounts []wire.Account
	h.doJSON("GET", "/accounts?userId=u1", nil, &accounts, 200)
	if len(accounts) != 0 {
		t.Fatalf("accounts after logout %+v", accounts)
	}
	h.doJSON("GET", "/accounts/"+key+"/health", nil, nil, 404)

	h.doJSON("DELETE", "/accounts/"+key+"/data", nil, nil, 204)
	ctx := context.Background()
	evts, err := h.reg.Store.ReadEvents(ctx, key, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evts {
		if e.Type != wire.EventAccountStatus {
			t.Fatalf("event left after data deletion: %+v", e)
		}
	}
	if ok, _ := h.reg.Store.ConversationExists(ctx, key, "dm-alice"); ok {
		t.Fatal("conversation left after data deletion")
	}
	portal, err := h.reg.Get("fake").Bridge.GetExistingPortalByKey(ctx, networkid.PortalKey{ID: "dm-alice", Receiver: "15550001111"})
	if err != nil || portal != nil {
		t.Fatalf("portal left after data deletion: %v %v", portal, err)
	}
}

func TestUsersAreIsolated(t *testing.T) {
	h := newHarness(t)
	keyA, clientA := h.login("user-a", "+15550000001")
	keyB, _ := h.login("user-b", "+15550000002")
	connA := h.dial(keyA, nil)
	connB := h.dial(keyB, nil)
	next(t, connA)
	next(t, connB)

	clientA.Incoming("dm-alice", "alice", "secret-1", "for A only")
	until(t, connA, wire.EventMessageUpsert)

	evts, err := h.reg.Store.ReadEvents(context.Background(), keyB, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evts {
		if e.Type == wire.EventMessageUpsert || e.Type == wire.EventConversationUpsert {
			t.Fatalf("user B received %+v", e)
		}
	}
	var accounts []wire.Account
	h.doJSON("GET", "/accounts?userId=user-b", nil, &accounts, 200)
	if len(accounts) != 1 || accounts[0].LoginID != keyB {
		t.Fatalf("user-b accounts %+v", accounts)
	}
	h.doJSON("POST", "/accounts/"+keyB+"/history", map[string]any{"conversationId": "dm-alice"}, nil, 404)
	h.doJSON("POST", "/logins/start", map[string]any{"network": "fake", "userId": "user-b", "flowId": "phone", "reconnectLoginId": keyA}, nil, 404)
}

func TestNewSubscriberReplacesOld(t *testing.T) {
	h := newHarness(t)
	key, _ := h.login("u1", "+15550001111")
	old := h.dial(key, nil)
	next(t, old)
	newer := h.dial(key, nil)
	next(t, newer)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var f frame
	err := wsjson.Read(ctx, old, &f)
	if websocket.CloseStatus(err) != 4000 {
		t.Fatalf("old subscriber: %v, want close 4000", err)
	}
}
