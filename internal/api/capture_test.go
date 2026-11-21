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
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

type captureStep struct {
	wire.Step
	Fields []wire.CookieField `json:"fields"`
}

type captureResponse struct {
	ProcessID string      `json:"processId"`
	Step      captureStep `json:"step"`
}

func (h *harness) startWeb(userID string) captureResponse {
	h.t.Helper()
	var r captureResponse
	h.doJSON("POST", "/logins/start", map[string]any{"network": "fake", "userId": userID, "flowId": "web"}, &r, 200)
	if r.Step.Kind != wire.KindWebSessionCapture || r.Step.StepID != fakenet.StepWeb {
		h.t.Fatalf("web flow first step %+v", r.Step.Step)
	}
	return r
}

func (h *harness) submit(processID string, body map[string]any) captureResponse {
	h.t.Helper()
	var r captureResponse
	h.doJSON("POST", "/logins/"+processID+"/submit", body, &r, 200)
	return r
}

func webValues(n string) map[string]string {
	return map[string]string{
		"session": "fake-session-" + n, "token": "fake-token-" + n, "track": "fake-track-" + n, "body": "fake-body-" + n,
	}
}

func TestFakeWebFlowStepPayload(t *testing.T) {
	h := newHarness(t)
	var nets []wire.Network
	h.doJSON("GET", "/networks", nil, &nets, 200)
	if len(nets[0].Flows) != 3 || nets[0].Flows[2].ID != "web" {
		t.Fatalf("flows %+v", nets[0].Flows)
	}
	code, raw := h.do("POST", "/logins/start", map[string]any{"network": "fake", "userId": "u1", "flowId": "web"})
	if code != 200 {
		t.Fatalf("start: %d %s", code, raw)
	}
	for _, key := range []string{`"url":`, `"extractJs":`, `"waitForUrlPattern":`, `"initialCookies":`, `"httpOnly":true`, `"cookieDomain":`, `"requestUrlRegex":`, `"required":true`, `"required":false`, `"pattern":`, `"sources":`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Errorf("step JSON lacks %s: %s", key, raw)
		}
	}
	var r captureResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	s := r.Step
	base := h.ts.URL
	host := strings.TrimPrefix(base, "http://")
	host = host[:strings.LastIndex(host, ":")]
	if s.URL != base+"/fake-provider/login" || s.JSRequired || s.Hidden || s.Pollable || s.ExtractJS == "" {
		t.Fatalf("step %+v", s.Step)
	}
	if !regexp.MustCompile(s.WaitForURLPattern).MatchString(base+"/fake-provider/home") || regexp.MustCompile(s.WaitForURLPattern).MatchString(s.URL) {
		t.Fatalf("waitForUrlPattern %q", s.WaitForURLPattern)
	}
	if len(s.InitialCookies) != 1 || s.InitialCookies[0].Name != fakenet.WebInitialCookieName || s.InitialCookies[0].Value == "" ||
		s.InitialCookies[0].Domain != host || !s.InitialCookies[0].HTTPOnly || s.InitialCookies[0].Secure {
		t.Fatalf("initialCookies %+v", s.InitialCookies)
	}
	want := []struct {
		id, sourceType, name string
		required             bool
	}{
		{"session", "cookie", fakenet.WebCookieName, true},
		{"token", "local_storage", fakenet.WebStorageKey, true},
		{"track", "request_header", fakenet.WebHeaderName, true},
		{"body", "request_body", fakenet.WebBodyField, true},
		{"ua", "special", "fake.ua", false},
	}
	if len(s.Fields) != len(want) {
		t.Fatalf("fields %+v", s.Fields)
	}
	patterns := 0
	for i, w := range want {
		f := s.Fields[i]
		if f.ID != w.id || f.Required != w.required || len(f.Sources) != 1 || f.Sources[0].Type != w.sourceType || f.Sources[0].Name != w.name {
			t.Errorf("field %d: %+v", i, f)
		}
		if f.Pattern != "" {
			patterns++
		}
		switch w.sourceType {
		case "cookie":
			if f.Sources[0].CookieDomain != host {
				t.Errorf("cookieDomain %q", f.Sources[0].CookieDomain)
			}
		case "request_header", "request_body":
			if !regexp.MustCompile(f.Sources[0].RequestURLRegex).MatchString(base + "/fake-provider/api/ping") {
				t.Errorf("requestUrlRegex %q", f.Sources[0].RequestURLRegex)
			}
		}
	}
	if patterns != 1 || !regexp.MustCompile(s.Fields[0].Pattern).MatchString("fake-session-1001") {
		t.Errorf("patterns: %d, session pattern %q", patterns, s.Fields[0].Pattern)
	}
}

// The provider pages are the shared test target of the clients: the test walks
// them like a browser (without JavaScript) and submits what a capture would read.
func TestFakeWebFlowEndToEnd(t *testing.T) {
	h := newHarness(t)
	r := h.startWeb("u1")

	jar, _ := cookiejar.New(nil)
	var hops []*http.Response
	browser := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		hops = append(hops, req.Response)
		return nil
	}}
	get := func(u string) (*http.Response, string) {
		t.Helper()
		resp, err := browser.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, string(raw)
	}

	resp, page := get(r.Step.URL)
	if resp.StatusCode != 200 || !strings.Contains(page, `action="/fake-provider/login"`) || !strings.Contains(page, `name="account"`) {
		t.Fatalf("login page: %d %s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "initial cookie: absent") {
		t.Fatalf("login page without the initial cookie: %s", page)
	}
	resp, err := browser.PostForm(h.ts.URL+"/fake-provider/login", url.Values{"account": {"1001"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	home := string(raw)
	if len(hops) != 1 || hops[0].StatusCode != http.StatusSeeOther {
		t.Fatalf("POST login did not redirect once: %d hops", len(hops))
	}
	var session *http.Cookie
	for _, c := range hops[0].Cookies() {
		if c.Name == fakenet.WebCookieName {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.Value != "fake-session-1001" || session.Path != "/fake-provider" {
		t.Fatalf("session cookie %+v", session)
	}
	finalURL := resp.Request.URL.String()
	if resp.StatusCode != 200 || !regexp.MustCompile(r.Step.WaitForURLPattern).MatchString(finalURL) {
		t.Fatalf("home: %d at %s", resp.StatusCode, finalURL)
	}
	for _, want := range []string{
		`localStorage.setItem("fake_token", "fake-token-1001")`,
		`"X-Fake-Track": "fake-track-1001"`,
		`fake_body: "fake-body-1001"`,
		`fetch("/fake-provider/api/ping"`,
	} {
		if !strings.Contains(home, want) {
			t.Errorf("home page lacks %s:\n%s", want, home)
		}
	}

	ping := func(track, body string) int {
		req, _ := http.NewRequest("POST", h.ts.URL+"/fake-provider/api/ping", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if track != "" {
			req.Header.Set(fakenet.WebHeaderName, track)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := ping("fake-track-1001", `{"fake_body":"fake-body-1001"}`); code != 200 {
		t.Fatalf("ping: %d", code)
	}
	if code := ping("", `{"fake_body":"fake-body-1001"}`); code != 400 {
		t.Fatalf("ping without the header: %d", code)
	}

	// A browser that got the initial cookie of the step sees it on the login page.
	u, _ := url.Parse(r.Step.URL)
	ic := r.Step.InitialCookies[0]
	jar.SetCookies(u, []*http.Cookie{{Name: ic.Name, Value: ic.Value, Path: ic.Path, HttpOnly: ic.HTTPOnly}})
	if _, page = get(r.Step.URL); !strings.Contains(page, "initial cookie: present") {
		t.Fatalf("login page with the initial cookie: %s", page)
	}

	done := h.submit(r.ProcessID, map[string]any{"stepId": r.Step.StepID, "cookies": map[string]string{
		"session": session.Value, "token": "fake-token-1001", "track": "fake-track-1001", "body": "fake-body-1001", "ua": "Test/1.0",
	}})
	if done.Step.Kind != wire.KindDone || done.Step.LoginID != "fake:web-1001" || done.Step.RemoteID != "web-1001" {
		t.Fatalf("after submit: %+v", done.Step.Step)
	}
	var accounts []wire.Account
	h.doJSON("GET", "/accounts?userId=u1", nil, &accounts, 200)
	if len(accounts) != 1 || accounts[0].LoginID != "fake:web-1001" || accounts[0].State != "connected" {
		t.Fatalf("accounts %+v", accounts)
	}
}

func TestFakeProviderAccess(t *testing.T) {
	h := newHarness(t)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, h.ts.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	if code, _ := status("GET", "/fake-provider/login", nil); code != 200 {
		t.Errorf("login page without a token: %d", code)
	}
	if code, _ := status("GET", "/networks", nil); code != 401 {
		t.Errorf("host route without a token: %d", code)
	}
	if code, _ := status("POST", "/fake/capture-url", nil); code != 401 {
		t.Errorf("fake control route without a token: %d", code)
	}
	if code, loc := status("GET", "/fake-provider/home", nil); code != http.StatusSeeOther || loc != "/fake-provider/login" {
		t.Errorf("home without a session: %d %q", code, loc)
	}
	if code, loc := status("POST", "/fake-provider/login", url.Values{"account": {"abc"}}); code != http.StatusSeeOther || loc != "/fake-provider/login?error=account" {
		t.Errorf("bad account: %d %q", code, loc)
	}
}

func TestCaptureSubmitRejections(t *testing.T) {
	h := newHarness(t)
	r := h.startWeb("u1")
	edit := func(fn func(map[string]string)) map[string]string {
		m := webValues("7")
		fn(m)
		return m
	}
	tests := []struct {
		name string
		body map[string]any
	}{
		{"unknown id", map[string]any{"cookies": edit(func(m map[string]string) { m["extra"] = "x" })}},
		{"missing required", map[string]any{"cookies": edit(func(m map[string]string) { delete(m, "token") })}},
		{"empty required", map[string]any{"cookies": edit(func(m map[string]string) { m["track"] = "" })}},
		{"pattern mismatch", map[string]any{"cookies": edit(func(m map[string]string) { m["session"] = "prelogin" })}},
		{"over 64 KB", map[string]any{"cookies": edit(func(m map[string]string) { m["ua"] = strings.Repeat("a", 64*1024) })}},
		{"no input", map[string]any{}},
		{"junk paste", map[string]any{"paste": "not a capture"}},
		{"incomplete json paste", map[string]any{"paste": `{"session":"fake-session-7"}`}},
		{"curl paste cannot carry local storage", map[string]any{"paste": `curl 'https://example.com/' -b 'session=fake-session-7' -H 'X-Fake-Track: t' -H 'content-type: application/json' --data-raw '{"fake_body":"b"}'`}},
		{"paste and cookies", map[string]any{"paste": `{"session":"fake-session-7"}`, "cookies": webValues("7")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.body["stepId"] = r.Step.StepID
			got := h.submit(r.ProcessID, tc.body)
			if got.Step.StepID != r.Step.StepID || got.Step.Kind != wire.KindWebSessionCapture || got.Step.Error == nil || got.Step.Error.Code != "invalid_input" {
				t.Fatalf("step %+v error %+v", got.Step.Step, got.Step.Error)
			}
			if got.Step.URL != r.Step.URL || len(got.Step.Fields) != len(r.Step.Fields) {
				t.Fatalf("the rejected step lost its payload: %+v", got.Step.Step)
			}
		})
	}
	// The upstream step was not consumed: a good submit still completes the login.
	done := h.submit(r.ProcessID, map[string]any{"stepId": r.Step.StepID, "cookies": webValues("7")})
	if done.Step.Kind != wire.KindDone || done.Step.LoginID != "fake:web-7" || done.Step.Error != nil {
		t.Fatalf("after the rejections: %+v", done.Step.Step)
	}
}

func TestCaptureSubmitBodyCap(t *testing.T) {
	h := newHarness(t)
	r := h.startWeb("u1")
	code, raw := h.do("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "paste": strings.Repeat("a", 300*1024)})
	if code != 400 || !bytes.Contains(raw, []byte(`"invalid_input"`)) {
		t.Fatalf("oversized body: %d %s", code, raw)
	}
}

func TestCaptureSubmitPaste(t *testing.T) {
	h := newHarness(t)
	r := h.startWeb("u1")
	raw, _ := json.Marshal(webValues("42"))
	done := h.submit(r.ProcessID, map[string]any{"stepId": r.Step.StepID, "paste": "\n  " + string(raw) + "\n"})
	if done.Step.Kind != wire.KindDone || done.Step.LoginID != "fake:web-42" {
		t.Fatalf("json paste: %+v error %+v", done.Step.Step, done.Step.Error)
	}
}

func TestCaptureUpstreamRejection(t *testing.T) {
	h := newHarness(t)
	r := h.startWeb("u1")
	got := h.submit(r.ProcessID, map[string]any{"stepId": r.Step.StepID, "cookies": webValues("0")})
	if got.Step.Kind != wire.KindFailed || got.Step.Error == nil || got.Step.Error.Code != "internal_error" {
		t.Fatalf("rejected session: %+v error %+v", got.Step.Step, got.Step.Error)
	}
}

func TestFakeCaptureURL(t *testing.T) {
	h := newHarness(t)
	var set struct {
		URL string `json:"url"`
	}
	h.doJSON("POST", "/fake/capture-url", map[string]string{"url": "http://10.0.2.2:4000/fake-provider/login"}, &set, 200)
	r := h.startWeb("u1")
	if set.URL != r.Step.URL || r.Step.URL != "http://10.0.2.2:4000/fake-provider/login" {
		t.Fatalf("url %q, endpoint said %q", r.Step.URL, set.URL)
	}
	if d := r.Step.Fields[0].Sources[0].CookieDomain; d != "10.0.2.2" {
		t.Errorf("cookieDomain %q", d)
	}
	if re := r.Step.Fields[2].Sources[0].RequestURLRegex; !regexp.MustCompile(re).MatchString("http://10.0.2.2:4000/fake-provider/api/ping") {
		t.Errorf("requestUrlRegex %q", re)
	}
	if code, _ := h.do("POST", "/fake/capture-url", map[string]string{"url": "ftp://x"}); code != 400 {
		t.Errorf("bad url: %d", code)
	}
	h.doJSON("POST", "/fake/capture-url", map[string]string{"url": ""}, &set, 200)
	if set.URL != fakenet.DefaultCaptureURL {
		t.Errorf("reset gave %q", set.URL)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// No captured value, paste text or initial-cookie value may reach the log, at any level.
func TestCaptureValuesNeverLogged(t *testing.T) {
	out := &lockedBuffer{}
	log := zerolog.New(out).Level(zerolog.TraceLevel)
	fake := fakenet.New()
	reg, err := hosts.Start(log.WithContext(context.Background()), t.TempDir(), []hosts.Factory{
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
	h := &harness{t: t, ts: ts, reg: reg, fake: fake}

	secrets := []string{"fake-session-9001", "fake-session-9002", "fake-session-0"}
	mark := func(s string) string {
		s = "S3CR3T-" + s
		secrets = append(secrets, s)
		return s
	}
	values := func(n string) map[string]string {
		return map[string]string{"session": "fake-session-" + n, "token": mark("token-" + n), "track": mark("track-" + n), "body": mark("body-" + n), "ua": mark("ua-" + n)}
	}

	r := h.startWeb("u1")
	secrets = append(secrets, r.Step.InitialCookies[0].Value)
	step := r.Step.StepID
	bad := values("9001")
	bad["session"] = mark("prelogin")
	h.submit(r.ProcessID, map[string]any{"stepId": step, "cookies": bad})
	unknown := values("9001")
	unknown[mark("unknown-id")] = mark("unknown-value")
	h.submit(r.ProcessID, map[string]any{"stepId": step, "cookies": unknown})
	h.submit(r.ProcessID, map[string]any{"stepId": step, "paste": mark("junk paste")})
	h.submit(r.ProcessID, map[string]any{"stepId": step, "paste": `curl 'https://example.com/` + mark("curl-url") + `' -b 'session=` + mark("curl-cookie") + `' -H 'X-Fake-Track: ` + mark("curl-header") + `'`})
	h.submit(r.ProcessID, map[string]any{"stepId": step, "paste": `curl 'https://example.com/' -H '` + mark("curl-broken") + `'`})
	h.submit(r.ProcessID, map[string]any{"stepId": step, "paste": `{"session":"` + mark("json-paste") + `"}`})
	if code, _ := h.do("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": step, "cookies": map[string]any{"session": 9001, "token": mark("typed")}}); code != 400 {
		t.Fatalf("wrong value type: %d", code)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/logins/"+r.ProcessID+"/submit", strings.NewReader(`{"stepId":"`+step+`","paste":"`+mark("broken-json")))
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
		t.Fatalf("broken body: %v", err)
	} else {
		resp.Body.Close()
	}
	if done := h.submit(r.ProcessID, map[string]any{"stepId": step, "cookies": values("9001")}); done.Step.Kind != wire.KindDone {
		t.Fatalf("good submit: %+v", done.Step.Step)
	}

	r = h.startWeb("u1")
	raw, _ := json.Marshal(values("9002"))
	if done := h.submit(r.ProcessID, map[string]any{"stepId": step, "paste": string(raw)}); done.Step.Kind != wire.KindDone {
		t.Fatalf("paste submit: %+v", done.Step.Step)
	}

	// The fake network quotes the session in its error, like an upstream connector could.
	r = h.startWeb("u1")
	failed := h.submit(r.ProcessID, map[string]any{"stepId": step, "cookies": values("0")})
	if failed.Step.Kind != wire.KindFailed {
		t.Fatalf("rejected session: %+v", failed.Step.Step)
	}

	logged := out.String()
	for _, want := range []string{"Login step failed", `"kind":"web_session_capture"`, `"kind":"done"`} {
		if !strings.Contains(logged, want) {
			t.Errorf("the log lacks %q, so this test does not see the login log lines", want)
		}
	}
	for _, s := range secrets {
		if strings.Contains(logged, s) {
			i := strings.Index(logged, s)
			line := logged[strings.LastIndex(logged[:i], "\n")+1:]
			if end := strings.Index(line, "\n"); end >= 0 {
				line = line[:end]
			}
			t.Errorf("the log has a submitted value in the line of length %d that starts %.80q", len(line), line)
		}
	}
}
