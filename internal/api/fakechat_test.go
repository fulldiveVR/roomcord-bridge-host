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
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	fcconnector "github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/connector"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/media"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
	fcserver "github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/server"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/localmatrix"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

const fcControl = "test-control"

// fcHarness runs a FakeChat server (its own HTTP listener) and the host with the
// fakechat connector pointed at it. The connector reaches FakeChat only over HTTP
// and WebSocket.
type fcHarness struct {
	*harness
	fc   *httptest.Server
	conn *fcconnector.Connector
}

func newFCHarness(t *testing.T, qrTTL time.Duration) *fcHarness {
	t.Helper()
	fcSrv := httptest.NewServer(fcserver.New(fcserver.Config{ControlToken: fcControl, Seed: 7, Deterministic: true, QRTTL: qrTTL, Log: zerolog.Nop()}).Handler())
	conn := fcconnector.New(fcconnector.Config{URL: fcSrv.URL})
	log := zerolog.Nop()
	reg, err := hosts.Start(context.Background(), t.TempDir(), []hosts.Factory{
		{ID: fcconnector.NetworkID, New: func() (bridgev2.NetworkConnector, error) { return conn, nil }},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Reg: reg, Logins: login.NewEngine(reg, log), Token: token, Version: "test", Log: log}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		reg.Stop()
		fcSrv.CloseClientConnections()
		fcSrv.Close()
	})
	return &fcHarness{harness: &harness{t: t, ts: ts, reg: reg}, fc: fcSrv, conn: conn}
}

func (h *fcHarness) control(method, path string, body, into any, want int) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.fc.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+fcControl)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		h.t.Fatalf("control %s %s: %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			h.t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

func (h *fcHarness) start(flow string, extra map[string]any) wire.LoginResponse {
	h.t.Helper()
	body := map[string]any{"network": fcconnector.NetworkID, "userId": "u1", "flowId": flow}
	for k, v := range extra {
		body[k] = v
	}
	var r wire.LoginResponse
	h.doJSON("POST", "/logins/start", body, &r, 200)
	return r
}

func (h *fcHarness) submit(r wire.LoginResponse, fields map[string]string) wire.LoginResponse {
	h.t.Helper()
	var out wire.LoginResponse
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": fields}, &out, 200)
	return out
}

func (h *fcHarness) passwordLogin(username, password string, extra map[string]any) string {
	h.t.Helper()
	r := h.submit(h.start("password", extra), map[string]string{"username": username, "password": password})
	if r.Step.Kind != wire.KindDone {
		h.t.Fatalf("password login: %+v", r.Step)
	}
	return r.Step.LoginID
}

func (h *fcHarness) waitConversations(loginKey string, n int) []wire.Conversation {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var page wire.Page[wire.Conversation]
		code, raw := h.do("GET", "/accounts/"+loginKey+"/conversations", nil)
		if code == 200 && json.Unmarshal(raw, &page) == nil && len(page.Items) >= n {
			return page.Items
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("conversations: %d %s, want %d", code, raw, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *fcHarness) waitHealth(loginKey, state string) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var hl wire.Health
		h.doJSON("GET", "/accounts/"+loginKey+"/health", nil, &hl, 200)
		if hl.State == state {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("health %s, want %s", hl.State, state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *fcHarness) sent(accountID string) []proto.SentRecord {
	var out []proto.SentRecord
	h.control("GET", "/control/accounts/"+accountID+"/sent", nil, &out, 200)
	return out
}

func (h *fcHarness) waitSent(accountID string, match func(proto.SentRecord) bool) proto.SentRecord {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, r := range h.sent(accountID) {
			if match(r) {
				return r
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no matching sent record in %+v", h.sent(accountID))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// frames reads the host event stream of one login in the background.
type frames struct {
	t    *testing.T
	mu   sync.Mutex
	all  []frame
	more chan struct{}
}

func (h *fcHarness) stream(loginKey string) *frames {
	h.t.Helper()
	zero := int64(0)
	conn := h.dial(loginKey, &zero)
	f := &frames{t: h.t, more: make(chan struct{}, 1)}
	go func() {
		for {
			var fr frame
			if err := wsjson.Read(context.Background(), conn, &fr); err != nil {
				return
			}
			f.mu.Lock()
			f.all = append(f.all, fr)
			f.mu.Unlock()
			select {
			case f.more <- struct{}{}:
			default:
			}
		}
	}()
	return f
}

func (f *frames) wait(what string, match func(frame) bool) frame {
	f.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		f.mu.Lock()
		for _, fr := range f.all {
			if match(fr) {
				f.mu.Unlock()
				return fr
			}
		}
		f.mu.Unlock()
		select {
		case <-f.more:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			f.t.Fatalf("no frame: %s", what)
		}
	}
}

func (f *frames) message(id string, match func(wire.Message) bool) wire.Message {
	f.t.Helper()
	var out wire.Message
	f.wait("message "+id, func(fr frame) bool {
		if fr.Type != wire.EventMessageUpsert {
			return false
		}
		var p wire.MessageUpsertPayload
		if json.Unmarshal(fr.Payload, &p) != nil || p.Message.ExternalMessageID != id {
			return false
		}
		if match != nil && !match(p.Message) {
			return false
		}
		out = p.Message
		return true
	})
	return out
}

func TestFakeChatNetworkListed(t *testing.T) {
	h := newFCHarness(t, 0)
	var nets []wire.Network
	h.doJSON("GET", "/networks", nil, &nets, 200)
	if len(nets) != 1 || nets[0].ID != "fakechat" || !nets[0].Available || len(nets[0].Flows) != 4 {
		t.Fatalf("networks %+v", nets)
	}
	got := []string{}
	for _, f := range nets[0].Flows {
		got = append(got, f.ID)
	}
	if strings.Join(got, ",") != "qr,phone,password,web" {
		t.Fatalf("flows %v", got)
	}
}

func TestFakeChatLoginPassword(t *testing.T) {
	h := newFCHarness(t, 0)
	r := h.start("password", nil)
	if r.Step.Kind != wire.KindFormInput || r.Step.StepID != fcconnector.StepCredentials {
		t.Fatalf("first step %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"username": "demo", "password": "wrong"})
	if r.Step.Kind != wire.KindFormInput || r.Step.Error == nil || r.Step.Error.Code != "invalid_input" || r.Step.StepID != fcconnector.StepCredentials {
		t.Fatalf("wrong password %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"username": "demo", "password": fcserver.DemoPassword})
	if r.Step.Kind != wire.KindDone || r.Step.LoginID != "fakechat:demo" || r.Step.RemoteName != "Demo User" {
		t.Fatalf("done %+v", r.Step)
	}
	h.waitHealth("fakechat:demo", "connected")

	r = h.start("password", nil)
	r = h.submit(r, map[string]string{"username": "secure", "password": fcserver.SecurePassword})
	if r.Step.Kind != wire.KindCodeInput || r.Step.FieldID != "code" {
		t.Fatalf("2fa step %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"code": "000000"})
	if r.Step.Kind != wire.KindCodeInput || r.Step.Error == nil || r.Step.Error.Code != "invalid_code" {
		t.Fatalf("wrong 2fa %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"code": fcserver.Secure2FA})
	if r.Step.Kind != wire.KindDone || r.Step.LoginID != "fakechat:secure" {
		t.Fatalf("2fa done %+v", r.Step)
	}
}

func TestFakeChatLoginPhone(t *testing.T) {
	h := newFCHarness(t, 0)
	r := h.start("phone", nil)
	if r.Step.Kind != wire.KindPhoneNumberInput || r.Step.FieldID != "phone" {
		t.Fatalf("first step %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"phone": "+9991234567"})
	if r.Step.Kind != wire.KindPhoneNumberInput || r.Step.Error == nil || r.Step.Error.Code != "invalid_phone_number" {
		t.Fatalf("invalid phone %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"phone": "+15550123456"})
	if r.Step.Kind != wire.KindCodeInput {
		t.Fatalf("code step %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"code": "111111"})
	if r.Step.Kind != wire.KindCodeInput || r.Step.Error == nil || r.Step.Error.Code != "invalid_code" || !strings.Contains(r.Step.Error.Message, "2 attempts") {
		t.Fatalf("wrong code %+v", r.Step)
	}
	r = h.submit(r, map[string]string{"code": fcserver.SMSCode})
	if r.Step.Kind != wire.KindDone || r.Step.LoginID != "fakechat:p15550123456" {
		t.Fatalf("done %+v", r.Step)
	}
	h.waitConversations(r.Step.LoginID, 4)

	// Three wrong codes end the challenge: the login fails.
	r = h.submit(h.start("phone", nil), map[string]string{"phone": "+15550123457"})
	for range 2 {
		r = h.submit(r, map[string]string{"code": "000000"})
	}
	r = h.submit(r, map[string]string{"code": "000000"})
	if r.Step.Kind != wire.KindFailed {
		t.Fatalf("after 3 wrong codes %+v", r.Step)
	}
}

func (h *fcHarness) pollLogin(id string, match func(wire.Step) bool, allowFailed ...bool) wire.Step {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var r wire.LoginResponse
		h.doJSON("GET", "/logins/"+id, nil, &r, 200)
		if match(r.Step) {
			return r.Step
		}
		if time.Now().After(deadline) || (r.Step.Kind == wire.KindFailed && len(allowFailed) == 0) {
			h.t.Fatalf("login step %+v %+v", r.Step, r.Step.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFakeChatLoginQR(t *testing.T) {
	h := newFCHarness(t, 1500*time.Millisecond)
	r := h.start("qr", nil)
	if r.Step.Kind != wire.KindQR || !r.Step.Pollable || !strings.HasPrefix(r.Step.Data, "fakechat://link?qr=") {
		t.Fatalf("qr step %+v", r.Step)
	}
	// The code expires and the step shows a new one with the same step id.
	first := r.Step.Data
	refreshed := h.pollLogin(r.ProcessID, func(s wire.Step) bool { return s.Kind == wire.KindQR && s.Data != first })
	if refreshed.StepID != r.Step.StepID {
		t.Fatalf("refresh changed the step id: %s", refreshed.StepID)
	}
	var pending []proto.PendingQR
	h.control("GET", "/control/qr", nil, &pending, 200)
	if len(pending) != 1 || pending[0].Data != refreshed.Data {
		t.Fatalf("pending %+v, step data %s", pending, refreshed.Data)
	}
	// Approve on the phone page, like a scan.
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).
		PostForm(h.fc.URL+"/phone/approve", url.Values{"qr": {pending[0].QRID}, "account": {"secure"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("phone approve %d", resp.StatusCode)
	}
	done := h.pollLogin(r.ProcessID, func(s wire.Step) bool { return s.Kind == wire.KindDone })
	if done.LoginID != "fakechat:secure" {
		t.Fatalf("done %+v", done)
	}
}

// Unscanned codes are replaced fcconnector.QRRefreshes times, then the login fails as expired.
func TestFakeChatLoginQRExpires(t *testing.T) {
	h := newFCHarness(t, 200*time.Millisecond)
	r := h.start("qr", nil)
	st := h.pollLogin(r.ProcessID, func(s wire.Step) bool { return s.Kind == wire.KindFailed }, true)
	if st.Error == nil || st.Error.Code != "expired" {
		t.Fatalf("expired %+v", st.Error)
	}
}

// TestFakeChatLoginWeb plays the desktop login window: it opens the capture URL,
// signs in on the page, follows the redirect, reads the httpOnly cookie from the
// cookie store and submits it.
func TestFakeChatLoginWeb(t *testing.T) {
	h := newFCHarness(t, 0)
	r := h.start("web", nil)
	st := r.Step
	if st.Kind != wire.KindWebSessionCapture || st.URL != h.fc.URL+"/web/login" || st.JSRequired || st.WaitForURLPattern == "" {
		t.Fatalf("capture step %+v", st)
	}
	raw, _ := json.Marshal(st.Fields)
	var fields []wire.CookieField
	_ = json.Unmarshal(raw, &fields)
	if len(fields) != 2 || fields[0].ID != "session" || !fields[0].Required || fields[0].Sources[0].Name != "fc_session" || fields[0].Sources[0].CookieDomain != "127.0.0.1" {
		t.Fatalf("fields %s", raw)
	}

	capture := func(username, password, code string) map[string]string {
		jar, _ := cookiejar.New(nil)
		browser := &http.Client{Jar: jar}
		page, err := browser.Get(st.URL)
		if err != nil {
			t.Fatal(err)
		}
		page.Body.Close()
		resp, err := browser.PostForm(h.fc.URL+"/web/login", url.Values{"username": {username}, "password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if code != "" {
			m := regexp.MustCompile(`name="challenge" value="([^"]+)"`).FindSubmatch(body)
			if m == nil {
				t.Fatalf("no 2fa form: %s", body)
			}
			resp, err = browser.PostForm(h.fc.URL+"/web/2fa", url.Values{"challenge": {string(m[1])}, "code": {code}})
			if err != nil {
				t.Fatal(err)
			}
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if !regexp.MustCompile(st.WaitForURLPattern).MatchString(resp.Request.URL.String()) || !bytes.Contains(body, []byte(`id="signed-in"`)) {
			t.Fatalf("final page %s: %s", resp.Request.URL, body)
		}
		out := map[string]string{}
		u, _ := url.Parse(h.fc.URL)
		for _, ck := range jar.Cookies(u) {
			switch ck.Name {
			case "fc_session":
				out["session"] = ck.Value
			case "fc_user":
				out["user"] = ck.Value
			}
		}
		return out
	}
	cookies := capture("demo", fcserver.DemoPassword, "")
	var done wire.LoginResponse
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": st.StepID, "cookies": cookies}, &done, 200)
	if done.Step.Kind != wire.KindDone || done.Step.LoginID != "fakechat:demo" {
		t.Fatalf("done %+v", done.Step)
	}

	// The 2FA account passes the code on the page.
	r = h.start("web", nil)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "cookies": capture("secure", fcserver.SecurePassword, fcserver.Secure2FA)}, &done, 200)
	if done.Step.Kind != wire.KindDone || done.Step.LoginID != "fakechat:secure" {
		t.Fatalf("2fa web done %+v", done.Step)
	}

	// A well-formed session that the network does not know fails the login.
	r = h.start("web", nil)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "cookies": map[string]string{"session": "fcs_0000"}}, &done, 200)
	if done.Step.Kind != wire.KindFailed {
		t.Fatalf("unknown session %+v", done.Step)
	}
	// A value that does not match the pattern never reaches the network.
	r = h.start("web", nil)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "cookies": map[string]string{"session": "nope"}}, &done, 200)
	if done.Step.Kind != wire.KindWebSessionCapture || done.Step.Error == nil || done.Step.Error.Code != "invalid_input" {
		t.Fatalf("bad pattern %+v", done.Step)
	}
}

func TestFakeChatBackfill(t *testing.T) {
	h := newFCHarness(t, 0)
	key := h.passwordLogin("demo", fcserver.DemoPassword, nil)
	convs := h.waitConversations(key, 4)
	byID := map[string]wire.Conversation{}
	for _, c := range convs {
		byID[c.ExternalConversationID] = c
	}
	if c := byID["dm-alice"]; c.Kind != "dm" || c.Title != "Alice Andersen" {
		t.Fatalf("dm-alice %+v", c)
	}
	if c := byID["grp-project"]; c.Kind != "group" || c.Title != "Project X" || len(c.ParticipantExternalIDs) != 4 {
		t.Fatalf("grp-project %+v", c)
	}
	history := func(conv string, cursor *string) wire.Page[wire.Message] {
		var page wire.Page[wire.Message]
		h.doJSON("POST", "/accounts/"+key+"/history", map[string]any{"conversationId": conv, "limit": 200, "cursor": cursor}, &page, 200)
		return page
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(history("grp-project", nil).Items) < 50 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	project := history("grp-project", nil)
	if len(project.Items) != 50 {
		t.Fatalf("project history %d messages, want the 50 of the initial backfill", len(project.Items))
	}
	if project.Items[0].Text != "Project update 60" || project.Items[49].Text != "Project update 11" {
		t.Fatalf("project order %q .. %q", project.Items[0].Text, project.Items[49].Text)
	}
	alice := history("dm-alice", nil)
	if len(alice.Items) != 5 {
		t.Fatalf("alice history %d", len(alice.Items))
	}
	outbound := 0
	for _, m := range alice.Items {
		if m.Direction == "outbound" {
			outbound++
			if m.SenderExternalID != "demo" {
				t.Fatalf("outbound sender %s", m.SenderExternalID)
			}
		} else if m.SenderExternalID != "alice" || m.SenderName == nil || *m.SenderName != "Alice Andersen" {
			t.Fatalf("inbound %+v", m)
		}
	}
	if outbound != 1 {
		t.Fatalf("outbound %d", outbound)
	}
	// Small pages walk the same history.
	var page wire.Page[wire.Message]
	h.doJSON("POST", "/accounts/"+key+"/history", map[string]any{"conversationId": "dm-alice", "limit": 2}, &page, 200)
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page 1 %+v", page)
	}
}

func attachment(t *testing.T, m wire.Message) wire.Attachment {
	t.Helper()
	if len(m.Attachments) != 1 {
		t.Fatalf("message %s has %d attachments", m.ExternalMessageID, len(m.Attachments))
	}
	return m.Attachments[0]
}

func TestFakeChatReceiveAllTypes(t *testing.T) {
	h := newFCHarness(t, 0)
	key := h.passwordLogin("demo", fcserver.DemoPassword, nil)
	h.waitConversations(key, 4)
	fr := h.stream(key)
	var res proto.ScenarioResult
	h.control("POST", "/control/accounts/demo/scenario", proto.ScenarioRequest{Name: "all-types"}, &res, 200)
	step := map[string]proto.ScenarioStep{}
	for _, s := range res.Steps {
		step[s.Name] = s
	}
	msg := func(name string) wire.Message { return fr.message(step[name].MessageID, nil) }

	if m := msg("text"); m.Text != "Hello from FakeChat" || m.SenderExternalID != "alice" || m.Direction != "inbound" {
		t.Fatalf("text %+v", m)
	}
	if m := msg("long_text"); len(m.Text) < 4000 {
		t.Fatalf("long text %d", len(m.Text))
	}
	if m := msg("formatted"); m.Text != "bold italic code link strike" {
		t.Fatalf("formatted %q", m.Text)
	}
	if m := msg("emoji_only"); m.Text != "🎉👍😀" {
		t.Fatalf("emoji %q", m.Text)
	}
	if m := msg("reply"); m.ReplyToExternalMessageID == nil || *m.ReplyToExternalMessageID != step["text"].MessageID || m.SenderExternalID != "carol" {
		t.Fatalf("reply %+v", m)
	}
	for name, want := range map[string]struct{ kind, mime, file string }{
		"image_jpg":  {"image", "image/jpeg", media.PhotoJPG},
		"image_png":  {"image", "image/png", media.PhotoPNG},
		"image_gif":  {"image", "image/gif", media.AnimGIF},
		"image_webp": {"image", "image/webp", media.PhotoWebP},
		"video":      {"video", "video/mp4", media.ClipMP4},
		"voice":      {"voice", "audio/ogg", media.VoiceOGG},
		"audio":      {"audio", "audio/wav", media.ToneWAV},
		"file_pdf":   {"file", "application/pdf", media.DocPDF},
		"file_zip":   {"file", "application/zip", media.ArchiveZIP},
		"sticker":    {"sticker", "image/webp", media.StickerWebP},
	} {
		m := msg(name)
		a := attachment(t, m)
		e, _ := media.Get(want.file)
		if a.Kind != want.kind || a.MimeType != want.mime || a.FileName == nil || *a.FileName != want.file || a.SizeBytes == nil || *a.SizeBytes != int64(len(e.Data())) {
			t.Fatalf("%s attachment %+v", name, a)
		}
		if e.Width > 0 && (a.Width == nil || *a.Width != e.Width) {
			t.Fatalf("%s width %+v", name, a.Width)
		}
		if e.DurationMs > 0 && name != "image_gif" && (a.DurationMs == nil || *a.DurationMs != e.DurationMs) {
			t.Fatalf("%s duration %+v", name, a.DurationMs)
		}
	}
	if m := msg("image_jpg"); m.Text != "A JPEG photo" {
		t.Fatalf("caption %q", m.Text)
	}
	// Media bytes come through the host unchanged.
	jpg := attachment(t, msg("image_jpg"))
	code, body := h.do("GET", fmt.Sprintf("/accounts/%s/media/%s/%s?conversationId=grp-project", key, step["image_jpg"].MessageID, jpg.ExternalAttachmentID), nil)
	if e, _ := media.Get(media.PhotoJPG); code != 200 || !bytes.Equal(body, e.Data()) {
		t.Fatalf("download %d, %d bytes", code, len(body))
	}
	if m := msg("location"); !strings.HasPrefix(m.Text, "Location: Eiffel Tower") || len(m.Attachments) != 0 {
		t.Fatalf("location %+v", m)
	}
	if a := attachment(t, msg("contact")); a.Kind != "file" || a.MimeType != "text/vcard" || *a.FileName != "Frank Fake.vcf" {
		t.Fatalf("contact %+v", a)
	}
	// Relay limit: one file exactly at 8 MiB, two over it. The host keeps the size.
	for name, size := range map[string]int64{"file_at_limit": media.RelayLimit, "file_oversize": 9 << 20, "image_oversize": 54 + 1800*1600*3} {
		a := attachment(t, msg(name))
		if a.SizeBytes == nil || *a.SizeBytes != size {
			t.Fatalf("%s size %v, want %d", name, a.SizeBytes, size)
		}
	}
	if a := attachment(t, msg("image_oversize")); a.Kind != "image" || a.MimeType != "image/bmp" || *a.SizeBytes <= media.RelayLimit {
		t.Fatalf("oversize image %+v", a)
	}
	big := attachment(t, msg("file_oversize"))
	code, body = h.do("GET", fmt.Sprintf("/accounts/%s/media/x/%s", key, big.ExternalAttachmentID), nil)
	if code != 200 || len(body) != 9<<20 {
		t.Fatalf("oversize download %d, %d bytes", code, len(body))
	}
	if m := msg("own_message"); m.Direction != "outbound" || m.SenderExternalID != "demo" {
		t.Fatalf("own message %+v", m)
	}
	fr.message(step["text"].MessageID, func(m wire.Message) bool {
		return m.EditedAt != nil && m.Text == "Hello from FakeChat (edited)"
	})
	reaction := func(typ, msgID, sender, emoji string) {
		fr.wait(typ+" "+emoji, func(f frame) bool {
			if f.Type != typ {
				return false
			}
			var p wire.ReactionPayload
			_ = json.Unmarshal(f.Payload, &p)
			return p.ExternalMessageID == msgID && p.Reaction.SenderExternalID == sender && strings.HasPrefix(p.Reaction.Emoji, emoji)
		})
	}
	jpgID := step["image_jpg"].MessageID
	reaction(wire.EventReactionUpsert, jpgID, "alice", "❤")
	reaction(wire.EventReactionUpsert, jpgID, "carol", "👍")
	reaction(wire.EventReactionDelete, jpgID, "carol", "👍")
	fr.wait("delete", func(f frame) bool {
		var p wire.MessageDeletePayload
		_ = json.Unmarshal(f.Payload, &p)
		return f.Type == wire.EventMessageDelete && p.ExternalMessageID == step["to_delete"].MessageID
	})
	fr.wait("read receipt", func(f frame) bool {
		var p wire.ReadReceiptPayload
		_ = json.Unmarshal(f.Payload, &p)
		return f.Type == wire.EventReadReceipt && p.ReaderExternalID == "alice" && p.LastReadExternalMessageID == step["own_message"].MessageID
	})
	fr.wait("member join", func(f frame) bool {
		var p wire.ParticipantUpsertPayload
		_ = json.Unmarshal(f.Payload, &p)
		return f.Type == wire.EventParticipantUpsert && p.ExternalConversationID == "grp-project" && p.Participant.ExternalParticipantID == "dave"
	})
	fr.wait("rename", func(f frame) bool {
		var p wire.ConversationUpsertPayload
		_ = json.Unmarshal(f.Payload, &p)
		return f.Type == wire.EventConversationUpsert && p.Conversation.Title == "Project X (renamed)"
	})
}

func (h *fcHarness) upload(key, name, mime string, data []byte) string {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename=%q`, name)}
	hdr["Content-Type"] = []string{mime}
	part, _ := mw.CreatePart(hdr)
	_, _ = part.Write(data)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", h.ts.URL+"/accounts/"+key+"/media", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		UploadRef string `json:"uploadRef"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.UploadRef == "" {
		h.t.Fatalf("upload: %d %v", resp.StatusCode, err)
	}
	return out.UploadRef
}

// queueMatrix sends a Matrix event of the login's own user into a portal, the way
// the host turns API calls into bridgev2 events.
func (h *fcHarness) queueMatrix(key, conv string, typ event.Type, content any, id string) {
	h.t.Helper()
	n := h.reg.Get(fcconnector.NetworkID)
	_, loginID, _ := login.ParseLoginKey(key)
	ul := n.Bridge.GetCachedUserLoginByID(loginID)
	portal, err := n.Bridge.GetExistingPortalByKey(context.Background(), networkid.PortalKey{ID: networkid.PortalID(conv), Receiver: loginID})
	if err != nil || portal == nil {
		h.t.Fatalf("portal %s: %v", conv, err)
	}
	evt := &event.Event{Sender: ul.UserMXID, Type: typ, Timestamp: time.Now().UnixMilli(), ID: localmatrix.OutgoingEventID(key, "test-"+id), RoomID: portal.MXID,
		Content: event.Content{Parsed: content}}
	if typ.Class == event.EphemeralEventType {
		evt.Mautrix.EventSource = event.SourceEphemeral
	}
	n.Bridge.QueueMatrixEvent(context.Background(), evt)
}

func (h *fcHarness) mxid(key, extID string) id.EventID {
	h.t.Helper()
	_, loginID, _ := login.ParseLoginKey(key)
	msg, err := h.reg.Get(fcconnector.NetworkID).Bridge.DB.Message.GetFirstPartByID(context.Background(), loginID, networkid.MessageID(extID))
	if err != nil || msg == nil {
		h.t.Fatalf("message %s: %v", extID, err)
	}
	return msg.MXID
}

func TestFakeChatSendAndActions(t *testing.T) {
	h := newFCHarness(t, 0)
	key := h.passwordLogin("demo", fcserver.DemoPassword, nil)
	h.waitConversations(key, 4)

	var sent wire.SendResponse
	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "c1", "text": "Hello Alice"}, &sent, 200)
	rec := h.waitSent("demo", func(r proto.SentRecord) bool { return r.Action == proto.SentSend && r.Text == "Hello Alice" })
	if rec.ChatID != "dm-alice" || rec.MessageID != sent.ExternalMessageID || rec.Kind != "text" {
		t.Fatalf("sent record %+v, response %+v", rec, sent)
	}
	// The same clientMessageId sends once.
	var again wire.SendResponse
	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "c1", "text": "Hello Alice"}, &again, 200)
	if again.ExternalMessageID != sent.ExternalMessageID {
		t.Fatalf("idempotent send %s != %s", again.ExternalMessageID, sent.ExternalMessageID)
	}

	// Media with a caption, as a reply.
	png, _ := media.Get(media.PhotoPNG)
	ref := h.upload(key, "upload.png", "image/png", png.Data())
	var mediaSent wire.SendResponse
	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "c2", "text": "a picture", "uploadRefs": []string{ref}, "replyToMessageId": sent.ExternalMessageID}, &mediaSent, 200)
	rec = h.waitSent("demo", func(r proto.SentRecord) bool { return r.Action == proto.SentSend && r.MediaID != "" })
	if rec.Kind != "image" || rec.Mime != "image/png" || rec.Size != int64(len(png.Data())) || rec.MediaName != "upload.png" || rec.Text != "a picture" || rec.ReplyTo != sent.ExternalMessageID {
		t.Fatalf("media record %+v", rec)
	}
	if len(mediaSent.Attachments) != 1 || mediaSent.Attachments[0].Kind != "image" {
		t.Fatalf("media response %+v", mediaSent)
	}

	// Read receipt.
	h.doJSON("POST", "/accounts/"+key+"/read", map[string]any{"conversationId": "dm-alice", "upToMessageId": sent.ExternalMessageID}, nil, 204)
	h.waitSent("demo", func(r proto.SentRecord) bool {
		return r.Action == proto.SentRead && r.MessageID == sent.ExternalMessageID
	})

	// Edit, react, unreact, delete and typing have no host route; they go through bridgev2 as Matrix events.
	target := h.mxid(key, sent.ExternalMessageID)
	edit := &event.MessageEventContent{MsgType: event.MsgText, Body: "* Hello Alice!", NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: "Hello Alice!"}}
	edit.RelatesTo = (&event.RelatesTo{}).SetReplace(target)
	h.queueMatrix(key, "dm-alice", event.EventMessage, edit, "edit1")
	h.waitSent("demo", func(r proto.SentRecord) bool { return r.Action == proto.SentEdit && r.Text == "Hello Alice!" })

	h.queueMatrix(key, "dm-alice", event.EventReaction, &event.ReactionEventContent{RelatesTo: event.RelatesTo{Type: event.RelAnnotation, EventID: target, Key: "👍"}}, "react1")
	h.waitSent("demo", func(r proto.SentRecord) bool { return r.Action == proto.SentReact && r.Emoji == "👍" })
	h.queueMatrix(key, "dm-alice", event.EventRedaction, &event.RedactionEventContent{Redacts: localmatrixReactionID(t, h, key, sent.ExternalMessageID)}, "unreact1")
	h.waitSent("demo", func(r proto.SentRecord) bool { return r.Action == proto.SentUnreact && r.Emoji == "👍" })

	h.queueMatrix(key, "dm-alice", event.EphemeralEventTyping, &event.TypingEventContent{UserIDs: []id.UserID{localmatrix.UserMXID("u1")}}, "typing1")
	h.waitSent("demo", func(r proto.SentRecord) bool { return r.Action == proto.SentTyping && r.Typing })

	h.queueMatrix(key, "dm-alice", event.EventRedaction, &event.RedactionEventContent{Redacts: target}, "delete1")
	h.waitSent("demo", func(r proto.SentRecord) bool {
		return r.Action == proto.SentDelete && r.MessageID == sent.ExternalMessageID
	})

	// A rate limit on send fails the send; the next one passes.
	h.control("POST", "/control/faults", proto.Fault{Kind: proto.FaultRateLimit, Scope: proto.ScopeSend}, nil, 200)
	code, raw := h.do("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-bob", "clientMessageId": "c3", "text": "limited"})
	if code != 502 || !strings.Contains(string(raw), "rate_limited") {
		t.Fatalf("rate limited send %d %s", code, raw)
	}
	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-bob", "clientMessageId": "c4", "text": "ok now"}, &sent, 200)
}

func localmatrixReactionID(t *testing.T, h *fcHarness, key, extID string) id.EventID {
	t.Helper()
	_, loginID, _ := login.ParseLoginKey(key)
	deadline := time.Now().Add(10 * time.Second)
	for {
		rs, err := h.reg.Get(fcconnector.NetworkID).Bridge.DB.Reaction.GetAllToMessage(context.Background(), loginID, networkid.MessageID(extID))
		if err == nil && len(rs) > 0 {
			return rs[0].MXID
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reaction on %s: %v", extID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFakeChatDisconnectAndRecovery(t *testing.T) {
	h := newFCHarness(t, 0)
	key := h.passwordLogin("demo", fcserver.DemoPassword, nil)
	h.waitConversations(key, 4)
	fr := h.stream(key)

	// The stream refuses the next two reconnects with 429 and 500.
	h.control("POST", "/control/faults", proto.Fault{Kind: proto.FaultRateLimit, Scope: proto.ScopeEvents}, nil, 200)
	h.control("POST", "/control/faults", proto.Fault{Kind: proto.FaultServerError, Scope: proto.ScopeEvents}, nil, 200)
	var closed map[string]int
	h.control("POST", "/control/accounts/demo/disconnect", nil, &closed, 200)
	if closed["closed"] != 1 {
		t.Fatalf("closed %v", closed)
	}
	fr.wait("connecting", func(f frame) bool {
		var p wire.AccountStatusPayload
		_ = json.Unmarshal(f.Payload, &p)
		return f.Type == wire.EventAccountStatus && p.Status == "connecting"
	})
	// A message while the client is offline arrives after the reconnect.
	var res proto.InjectResult
	h.control("POST", "/control/accounts/demo/inject", proto.Inject{ChatID: "dm-bob", Text: "while you were away"}, &res, 200)
	fr.message(res.Message.ID, nil)
	h.waitHealth(key, "connected")
	var accounts []proto.ControlAccount
	h.control("GET", "/control/accounts", nil, &accounts, 200)
	for _, a := range accounts {
		if a.ID == "demo" && a.Streams != 1 {
			t.Fatalf("streams %d", a.Streams)
		}
	}
}

func TestFakeChatRevokeAndRelogin(t *testing.T) {
	h := newFCHarness(t, 0)
	key := h.passwordLogin("demo", fcserver.DemoPassword, nil)
	h.waitConversations(key, 4)
	fr := h.stream(key)

	h.control("POST", "/control/accounts/demo/revoke", nil, nil, 204)
	fr.wait("needs_reauth", func(f frame) bool {
		var p wire.AccountStatusPayload
		_ = json.Unmarshal(f.Payload, &p)
		return f.Type == wire.EventAccountStatus && p.Status == "needs_reauth"
	})
	h.waitHealth(key, "needs_reauth")
	code, _ := h.do("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "x", "text": "x"})
	if code != 409 {
		t.Fatalf("send after revoke %d", code)
	}
	var res proto.InjectResult
	h.control("POST", "/control/accounts/demo/inject", proto.Inject{ChatID: "dm-alice", Text: "sent while revoked"}, &res, 200)

	again := h.passwordLogin("demo", fcserver.DemoPassword, map[string]any{"reconnectLoginId": key})
	if again != key {
		t.Fatalf("relogin %s", again)
	}
	h.waitHealth(key, "connected")
	fr.message(res.Message.ID, nil)
}
