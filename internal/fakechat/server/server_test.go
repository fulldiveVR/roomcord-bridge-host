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
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/media"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

const ctl = "ctl"

type tc struct {
	t  *testing.T
	ts *httptest.Server
}

func newTC(t *testing.T, seed int64) *tc {
	ts := httptest.NewServer(New(Config{ControlToken: ctl, Seed: seed, Deterministic: true}).Handler())
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return &tc{t: t, ts: ts}
}

func (c *tc) call(method, path, tok string, body, into any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.ts.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if into != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, into); err != nil {
			c.t.Fatalf("%s %s: %s", method, path, raw)
		}
	}
	return resp.StatusCode
}

func (c *tc) login(user, pass string) string {
	c.t.Helper()
	var res proto.PasswordResult
	if code := c.call("POST", "/api/v1/auth/password", "", proto.PasswordLogin{Username: user, Password: pass}, &res); code != 200 || res.AccessToken == "" {
		c.t.Fatalf("login %s: %d %+v", user, code, res)
	}
	return res.AccessToken
}

func TestMediaCatalog(t *testing.T) {
	sizes := map[string]int{media.LimitBin: media.RelayLimit, media.OversizeBin: 9 << 20, media.OversizePhoto: 54 + 1800*1600*3}
	for name, size := range sizes {
		e, _ := media.Get(name)
		if len(e.Data()) != size {
			t.Fatalf("%s: %d bytes, want %d", name, len(e.Data()), size)
		}
	}
	if e, _ := media.Get(media.OversizePhoto); len(e.Data()) <= media.RelayLimit {
		t.Fatal("the oversize photo is not over the relay limit")
	}
	get := func(name string) []byte { e, _ := media.Get(name); return e.Data() }
	if img, err := png.Decode(bytes.NewReader(get(media.PhotoPNG))); err != nil || img.Bounds().Dx() != 256 {
		t.Fatalf("png: %v", err)
	}
	if img, err := jpeg.Decode(bytes.NewReader(get(media.PhotoJPG))); err != nil || img.Bounds().Dx() != 320 {
		t.Fatalf("jpeg: %v", err)
	}
	if g, err := gif.DecodeAll(bytes.NewReader(get(media.AnimGIF))); err != nil || len(g.Image) != 3 {
		t.Fatalf("gif: %v", err)
	}
	for name, magic := range map[string][2]string{
		media.PhotoWebP: {"RIFF", "WEBP"}, media.StickerWebP: {"RIFF", "WEBP"}, media.ToneWAV: {"RIFF", "WAVE"},
	} {
		d := get(name)
		if string(d[:4]) != magic[0] || string(d[8:12]) != magic[1] {
			t.Fatalf("%s header %q", name, d[:12])
		}
	}
	if d := get(media.ClipMP4); string(d[4:8]) != "ftyp" {
		t.Fatalf("mp4 header %q", d[:8])
	}
	if d := get(media.VoiceOGG); string(d[:4]) != "OggS" {
		t.Fatalf("ogg header %q", d[:4])
	}
	if d := get(media.DocPDF); !bytes.HasPrefix(d, []byte("%PDF-1.4")) || !bytes.HasSuffix(d, []byte("%%EOF\n")) {
		t.Fatal("pdf framing")
	}
	zr, err := zip.NewReader(bytes.NewReader(get(media.ArchiveZIP)), int64(len(get(media.ArchiveZIP))))
	if err != nil || len(zr.File) != 2 {
		t.Fatalf("zip: %v", err)
	}
}

// The same seed gives the same ids, tokens and timestamps.
func TestDeterministicSeed(t *testing.T) {
	run := func() (string, proto.ScenarioResult, []proto.Message) {
		c := newTC(t, 42)
		tok := c.login("demo", DemoPassword)
		var res proto.ScenarioResult
		if code := c.call("POST", "/control/accounts/demo/scenario", ctl, proto.ScenarioRequest{Name: "burst", Count: 3, ChatID: ChatAlice}, &res); code != 200 {
			t.Fatalf("scenario %d", code)
		}
		var page proto.MessagePage
		c.call("GET", "/api/v1/chats/dm-alice/messages?limit=5", tok, nil, &page)
		return tok, res, page.Messages
	}
	tok1, res1, msgs1 := run()
	tok2, res2, msgs2 := run()
	if tok1 != tok2 {
		t.Fatalf("tokens %s != %s", tok1, tok2)
	}
	a, _ := json.Marshal([]any{res1, msgs1})
	b, _ := json.Marshal([]any{res2, msgs2})
	if !bytes.Equal(a, b) {
		t.Fatalf("runs differ:\n%s\n%s", a, b)
	}
}

func TestAuthFlows(t *testing.T) {
	c := newTC(t, 1)
	var e proto.ErrorBody
	if code := c.call("POST", "/api/v1/auth/password", "", proto.PasswordLogin{Username: "demo", Password: "x"}, &e); code != 401 || e.Error.Code != proto.ErrInvalidPassword {
		t.Fatalf("wrong password %d %+v", code, e)
	}
	var pr proto.PasswordResult
	c.call("POST", "/api/v1/auth/password", "", proto.PasswordLogin{Username: "secure", Password: SecurePassword}, &pr)
	if !pr.TwoFactorRequired || pr.AccessToken != "" {
		t.Fatalf("2fa %+v", pr)
	}
	var sess proto.Session
	if code := c.call("POST", "/api/v1/auth/2fa", "", proto.CodeSubmit{ChallengeID: pr.ChallengeID, Code: Secure2FA}, &sess); code != 200 || sess.Account.ID != "secure" {
		t.Fatalf("2fa submit %d %+v", code, sess)
	}

	if code := c.call("POST", "/api/v1/auth/phone", "", proto.PhoneStart{Phone: "+9990001112"}, &e); code != 400 || e.Error.Code != proto.ErrInvalidPhone {
		t.Fatalf("invalid phone %d", code)
	}
	var ch proto.Challenge
	c.call("POST", "/api/v1/auth/phone", "", proto.PhoneStart{Phone: "+15550109999"}, &ch)
	for left := CodeAttempts - 1; left > 0; left-- {
		e = proto.ErrorBody{}
		if code := c.call("POST", "/api/v1/auth/phone/verify", "", proto.CodeSubmit{ChallengeID: ch.ChallengeID, Code: "1"}, &e); code != 401 || *e.Error.AttemptsLeft != left {
			t.Fatalf("wrong code %d %+v", code, e)
		}
	}
	if code := c.call("POST", "/api/v1/auth/phone/verify", "", proto.CodeSubmit{ChallengeID: ch.ChallengeID, Code: "1"}, nil); code != 410 {
		t.Fatalf("last wrong code %d", code)
	}
	c.call("POST", "/api/v1/auth/phone", "", proto.PhoneStart{Phone: "+15550109999"}, &ch)
	if code := c.call("POST", "/api/v1/auth/phone/verify", "", proto.CodeSubmit{ChallengeID: ch.ChallengeID, Code: SMSCode}, &sess); code != 200 || sess.Account.ID != "p15550109999" {
		t.Fatalf("phone login %d %+v", code, sess)
	}
	if code := c.call("POST", "/api/v1/auth/web", "", proto.WebExchange{Session: "fcs_00"}, &e); code != 401 || e.Error.Code != proto.ErrInvalidSession {
		t.Fatalf("web exchange %d", code)
	}
	if code := c.call("GET", "/control/accounts", "", nil, nil); code != 401 {
		t.Fatalf("control without token %d", code)
	}
	if code := c.call("GET", "/api/v1/me", "nope", nil, nil); code != 401 {
		t.Fatalf("me with a bad token %d", code)
	}
}

func TestHistoryFaultsAndStream(t *testing.T) {
	c := newTC(t, 1)
	tok := c.login("demo", DemoPassword)
	var page proto.MessagePage
	c.call("GET", "/api/v1/chats/grp-project/messages?limit=10", tok, nil, &page)
	if len(page.Messages) != 10 || !page.HasMore || page.Messages[9].Text != "Project update 60" {
		t.Fatalf("newest page %+v", page)
	}
	var older proto.MessagePage
	c.call("GET", "/api/v1/chats/grp-project/messages?limit=10&before="+page.Messages[0].ID, tok, nil, &older)
	if len(older.Messages) != 10 || older.Messages[9].Text != "Project update 50" {
		t.Fatalf("older page %+v", older)
	}
	var newer proto.MessagePage
	c.call("GET", "/api/v1/chats/grp-project/messages?limit=10&after="+older.Messages[9].ID, tok, nil, &newer)
	if len(newer.Messages) != 10 || newer.HasMore || newer.Messages[0].Text != "Project update 51" {
		t.Fatalf("newer page %+v", newer)
	}

	c.call("POST", "/control/faults", ctl, proto.Fault{Kind: proto.FaultRateLimit, Scope: proto.ScopeHistory, Count: 2}, nil)
	for range 2 {
		if code := c.call("GET", "/api/v1/chats", tok, nil, nil); code != 429 {
			t.Fatalf("fault %d", code)
		}
	}
	if code := c.call("GET", "/api/v1/chats", tok, nil, nil); code != 200 {
		t.Fatalf("after faults %d", code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.ts.URL, "http")+"/api/v1/events?since=0", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := func() proto.Event {
		var e proto.Event
		if err := wsjson.Read(ctx, conn, &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	if e := read(); e.Type != proto.EventHello {
		t.Fatalf("first frame %+v", e)
	}
	// The stream skips what this session sent itself, and shows what others did.
	c.call("POST", "/api/v1/chats/dm-alice/messages", tok, proto.SendMessage{Text: "mine"}, nil)
	var inj proto.InjectResult
	c.call("POST", "/control/accounts/demo/inject", ctl, proto.Inject{ChatID: ChatAlice, Text: "from alice"}, &inj)
	if e := read(); e.Type != proto.EventMessage || e.Message.ID != inj.Message.ID || e.Seq != 2 {
		t.Fatalf("event %+v", e)
	}
	var sent []proto.SentRecord
	c.call("GET", "/control/accounts/demo/sent", ctl, nil, &sent)
	if len(sent) != 1 || sent[0].Text != "mine" {
		t.Fatalf("sent %+v", sent)
	}
	c.call("POST", "/control/accounts/demo/revoke", ctl, nil, nil)
	if e := read(); e.Type != proto.EventLoggedOut {
		t.Fatalf("after revoke %+v", e)
	}
	var e proto.Event
	if err := wsjson.Read(ctx, conn, &e); websocket.CloseStatus(err) != proto.CloseLoggedOut {
		t.Fatalf("close %v", err)
	}
	if code := c.call("GET", "/api/v1/me", tok, nil, nil); code != 401 {
		t.Fatalf("token after revoke %d", code)
	}
}

// Without a seed, ids carry the start time of the server state, so a restarted
// FakeChat never reuses an id that a bridge stored before.
func TestIDsDifferAcrossRestarts(t *testing.T) {
	first := New(Config{ControlToken: ctl})
	second := New(Config{ControlToken: ctl})
	a := first.accounts[DemoAccount].chats[ChatAlice].messages[0].ID
	b := second.accounts[DemoAccount].chats[ChatAlice].messages[0].ID
	if a == b || !strings.HasPrefix(a, "msg-") {
		t.Fatalf("ids %s and %s", a, b)
	}
	first.Reset(0)
	if c := first.accounts[DemoAccount].chats[ChatAlice].messages[0].ID; c == a {
		t.Fatalf("reset reused %s", c)
	}
}
