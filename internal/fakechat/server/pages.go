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
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// The web login (/web/*) is the target of the connector's web_session_capture
// step: a sign-in form whose success sets the httpOnly session cookie and
// redirects to /web/app. The phone page (/phone) approves QR codes, like a scan
// with the phone app.

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{font-family:system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem;color:#1d1d1f;background:#fafafa}
h1{font-size:1.4rem}label{display:block;margin:.8rem 0 .3rem}input,select{width:100%;padding:.5rem;box-sizing:border-box;font-size:1rem}
button{margin-top:1rem;padding:.6rem 1.2rem;font-size:1rem;background:#2563eb;color:#fff;border:0;border-radius:6px;cursor:pointer}
.error{color:#b91c1c}.note{color:#555;font-size:.9rem}li{margin:.8rem 0}code{background:#eee;padding:0 .2rem}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Error}}<p class="error" id="error">{{.Error}}</p>{{end}}
{{if eq .Kind "login"}}
<form method="post" action="/web/login" id="login-form">
<label for="username">Username</label><input id="username" name="username" autocomplete="username" value="{{.Username}}">
<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password">
<button id="sign-in" type="submit">Sign in</button>
</form>
<p class="note">Test accounts: <code>demo</code> / <code>demo-pass</code>, <code>secure</code> / <code>secure-pass</code> (code <code>654321</code>).</p>
{{else if eq .Kind "2fa"}}
<form method="post" action="/web/2fa" id="code-form">
<input type="hidden" name="challenge" value="{{.Challenge}}">
<label for="code">Two-factor code</label><input id="code" name="code" inputmode="numeric" autocomplete="one-time-code">
<button id="verify" type="submit">Verify</button>
</form>
{{else if eq .Kind "app"}}
<p id="signed-in">Signed in as <strong>{{.Name}}</strong> ({{.Username}}).</p>
<p class="note">You can close this window.</p>
{{else if eq .Kind "phone"}}
{{if .Approved}}<p id="approved">Approved {{.Approved}}.</p>{{end}}
{{if .QRs}}<ul id="pending">{{range .QRs}}<li><form method="post" action="/phone/approve">
<input type="hidden" name="qr" value="{{.QRID}}"><code>{{.QRID}}</code>
<label>Account</label><select name="account">{{range $.Accounts}}<option value="{{.}}"{{if eq . $.Username}} selected{{end}}>{{.}}</option>{{end}}</select>
<button type="submit">Approve</button></form></li>{{end}}</ul>
{{else}}<p id="none">No QR code is waiting. Start a QR login first.</p>{{end}}
{{end}}
</body></html>`))

type page struct {
	Title, Kind, Error, Username, Name, Challenge, Approved string
	QRs                                                     any
	Accounts                                                []string
}

func render(w http.ResponseWriter, status int, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, p)
}

func (s *Server) pageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /web/login", s.webLoginPage)
	mux.HandleFunc("POST /web/login", s.webLoginSubmit)
	mux.HandleFunc("POST /web/2fa", s.web2FASubmit)
	mux.HandleFunc("GET /web/app", s.webApp)
	mux.HandleFunc("GET /phone", s.phonePage)
	mux.HandleFunc("POST /phone/approve", s.phoneApprove)
}

func (s *Server) webLoginPage(w http.ResponseWriter, r *http.Request) {
	msg := ""
	switch r.URL.Query().Get("error") {
	case "credentials":
		msg = "Wrong username or password."
	case "code":
		msg = "Wrong code. Sign in again."
	}
	render(w, 200, page{Title: "Sign in to FakeChat", Kind: "login", Error: msg})
}

func (s *Server) setWebSession(w http.ResponseWriter, a *account) {
	tok := s.secret("fcs_")
	s.webSessions[tok] = a.ID
	http.SetCookie(w, &http.Cookie{Name: WebSessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: WebUserCookie, Value: a.ID, Path: "/", SameSite: http.SameSiteLaxMode})
}

func (s *Server) webLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.takeFault("auth") != nil {
		render(w, 503, page{Title: "Sign in to FakeChat", Kind: "login", Error: "The service is busy. Try again."})
		return
	}
	username, password := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password")
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findByUsername(username)
	if a == nil || a.password == "" || !constantEq(password, a.password) {
		http.Redirect(w, r, "/web/login?error=credentials", http.StatusSeeOther)
		return
	}
	if a.twoFactor != "" {
		c := &challenge{id: s.nextID("ch"), accountID: a.ID, attempts: 1, kind: "web2fa"}
		s.challenges[c.id] = c
		render(w, 200, page{Title: "Two-factor code", Kind: "2fa", Challenge: c.id})
		return
	}
	s.setWebSession(w, a)
	http.Redirect(w, r, "/web/app", http.StatusSeeOther)
}

func (s *Server) web2FASubmit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.challenges[r.PostFormValue("challenge")]
	if c == nil || c.kind != "web2fa" {
		http.Redirect(w, r, "/web/login?error=code", http.StatusSeeOther)
		return
	}
	delete(s.challenges, c.id)
	a := s.accounts[c.accountID]
	if a == nil || !constantEq(strings.TrimSpace(r.PostFormValue("code")), a.twoFactor) {
		http.Redirect(w, r, "/web/login?error=code", http.StatusSeeOther)
		return
	}
	s.setWebSession(w, a)
	http.Redirect(w, r, "/web/app", http.StatusSeeOther)
}

func (s *Server) webApp(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(WebSessionCookie)
	s.mu.Lock()
	var a *account
	if err == nil {
		a = s.accounts[s.webSessions[ck.Value]]
	}
	var p page
	if a != nil {
		p = page{Title: "FakeChat", Kind: "app", Name: a.DisplayName, Username: a.ID}
	}
	s.mu.Unlock()
	if a == nil {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	render(w, 200, p)
}

func (s *Server) phonePage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.accounts))
	for id := range s.accounts {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	sort.Strings(ids)
	selected := r.URL.Query().Get("account")
	if selected == "" {
		selected = DemoAccount
	}
	render(w, 200, page{Title: "FakeChat phone", Kind: "phone", QRs: s.pendingQRs(), Accounts: ids, Username: selected,
		Approved: r.URL.Query().Get("approved")})
}

func (s *Server) phoneApprove(w http.ResponseWriter, r *http.Request) {
	qr, acct := r.PostFormValue("qr"), r.PostFormValue("account")
	if err := s.ApproveQR(qr, acct); err != nil {
		render(w, 404, page{Title: "FakeChat phone", Kind: "phone", Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/phone?approved="+url.QueryEscape(qr)+"&account="+url.QueryEscape(acct), http.StatusSeeOther)
}
