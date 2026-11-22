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

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
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
{{end}}
</body></html>`))

// The phone page looks like the "Link a device" screen of a messenger app.
// Hooks for tests: #pending li per waiting code (its text has the code id),
// select[name=account], #link-device, #approved, #none, #error.
var phoneTmpl = template.Must(template.New("phone").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>Link a device · FakeChat</title>
<style>
:root{--bg:#f3f4f8;--panel:#fff;--text:#12151c;--muted:#586072;--line:rgba(18,28,58,.1);--fill:#eef0f5;
--accent:#2b5fd9;--accent-ink:#fff;--good:#1f8a4c;--good-soft:#e3f4ea;--bad:#c2341f;--bad-soft:#fde8e4}
@media (prefers-color-scheme:dark){:root{--bg:#0d0f14;--panel:#171a21;--text:#f1f3f8;--muted:#a7aebe;
--line:rgba(255,255,255,.09);--fill:#222630;--accent:#3a6be4;--good:#5fd08d;--good-soft:#173324;--bad:#ff8a75;--bad-soft:#3a1d18}}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--text);
font:16px/1.4 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased}
.app{max-width:440px;min-height:100vh;margin:0 auto;padding:0 20px 32px;display:flex;flex-direction:column}
header{display:flex;align-items:center;gap:10px;height:56px}
.glyph{width:30px;height:30px;border-radius:9px;background:#3a3f4b;display:grid;place-items:center;flex:none}
.brand{font-weight:700;font-size:17px;letter-spacing:-.01em}
h1{font-size:28px;line-height:1.15;letter-spacing:-.02em;margin:18px 0 6px}
.lead{color:var(--muted);margin:0 0 22px;font-size:15px}
ul{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:14px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:20px;padding:18px}
.device{display:flex;align-items:center;gap:14px;margin-bottom:16px}
.device .icon{width:48px;height:48px;border-radius:14px;background:var(--fill);display:grid;place-items:center;flex:none}
.device b{display:block;font-size:16px;font-weight:600}
.device small{display:block;color:var(--muted);font-size:13.5px;margin-top:2px}
.code{font:600 13px ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--fill);border-radius:6px;padding:1px 6px}
label{display:block;font-size:13px;font-weight:600;color:var(--muted);margin:0 0 6px}
.select{position:relative}
select{appearance:none;-webkit-appearance:none;width:100%;height:48px;border-radius:12px;border:1px solid var(--line);
background:var(--fill);color:var(--text);font:inherit;padding:0 40px 0 14px}
.select:after{content:"";position:absolute;right:16px;top:19px;width:7px;height:7px;border-right:2px solid var(--muted);
border-bottom:2px solid var(--muted);transform:rotate(45deg);pointer-events:none}
button{width:100%;height:50px;margin-top:14px;border:0;border-radius:14px;background:var(--accent);color:var(--accent-ink);
font-family:inherit;font-size:16px;font-weight:600;cursor:pointer}
button:active{filter:brightness(.92)}
.note{color:var(--muted);font-size:13px;text-align:center;margin:12px 0 0}
.state{text-align:center;padding:28px 18px}
.state .icon{width:64px;height:64px;border-radius:50%;margin:0 auto 14px;display:grid;place-items:center}
.state b{display:block;font-size:18px;margin-bottom:4px}
.state p{margin:0;color:var(--muted);font-size:14.5px}
#approved .icon{background:var(--good-soft);color:var(--good)}
#none .icon{background:var(--fill);color:var(--muted)}
.banner{border-radius:14px;padding:12px 14px;font-size:14.5px;margin:0 0 14px}
#error{background:var(--bad-soft);color:var(--bad)}
.spin{width:26px;height:26px;border-radius:50%;border:2.5px solid var(--line);border-top-color:var(--muted);animation:r 1s linear infinite}
@keyframes r{to{transform:rotate(360deg)}}
</style></head><body><div class="app">
<header><span class="glyph"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2"
stroke-linecap="round" stroke-linejoin="round"><path d="M4 5h16v11H9l-5 4z"/></svg></span><span class="brand">FakeChat</span></header>
<h1>Link a device</h1>
<p class="lead">Use FakeChat on your computer. Messages stay in sync on this phone.</p>
{{if .Error}}<p class="banner" id="error">{{.Error}}</p>{{end}}
{{if .Approved}}<div class="card state" id="approved"><div class="icon"><svg width="30" height="30" viewBox="0 0 24 24" fill="none"
stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg></div>
<b>Device linked</b><p>{{.ApprovedName}} is now signed in on your computer.</p></div>
{{else if .QRs}}<ul id="pending">{{range .QRs}}<li class="card" data-qr="{{.QRID}}">
<form method="post" action="/phone/approve">
<div class="device"><span class="icon"><svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor"
stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg></span>
<span><b>Computer wants to link</b><small>Code <span class="code">{{.QRID}}</span> · <span class="ttl" data-expires="{{.ExpiresAt.UTC.Format "2006-01-02T15:04:05Z07:00"}}"></span></small></span></div>
<input type="hidden" name="qr" value="{{.QRID}}">
<label for="account-{{.QRID}}">Link as</label>
<div class="select"><select id="account-{{.QRID}}" name="account">{{range $.Accounts}}<option value="{{.ID}}"{{if eq .ID $.Username}} selected{{end}}>{{.Name}}</option>{{end}}</select></div>
<button type="submit" id="link-device">Link device</button>
</form></li>{{end}}</ul>
<p class="note">Only link devices you own.</p>
{{else}}<div class="card state" id="none"><div class="icon"><div class="spin"></div></div>
<b>Waiting for a code</b><p>Open FakeChat on your computer and show the QR code. It appears here by itself.</p></div>{{end}}
</div>
<script>
(function(){
  var t=document.querySelectorAll('.ttl');
  function tick(){
    var now=Date.now(),gone=false;
    t.forEach(function(el){
      var s=Math.max(0,Math.round((Date.parse(el.dataset.expires)-now)/1000));
      if(!s)gone=true;
      el.textContent='expires in '+Math.floor(s/60)+':'+String(s%60).padStart(2,'0');
    });
    if(gone)location.replace('/phone');
  }
  if(t.length){tick();setInterval(tick,1000)}
  if(document.getElementById('none'))setTimeout(function(){location.reload()},2000);
})();
</script>
</body></html>`))

type page struct {
	Title, Kind, Error, Username, Name, Challenge, Approved string
}

type phoneAccount struct{ ID, Name string }

type phonePage struct {
	Error, Username, Approved, ApprovedName string
	QRs                                     []proto.PendingQR
	Accounts                                []phoneAccount
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
	selected := r.URL.Query().Get("account")
	if selected == "" {
		selected = DemoAccount
	}
	p := phonePage{QRs: s.pendingQRs(), Username: selected}
	if r.URL.Query().Get("approved") != "" {
		p.Approved = r.URL.Query().Get("approved")
		p.QRs = nil
	}
	s.renderPhone(w, 200, p)
}

func (s *Server) renderPhone(w http.ResponseWriter, status int, p phonePage) {
	s.mu.Lock()
	for id, a := range s.accounts {
		name := a.DisplayName
		if name == "" {
			name = id
		}
		p.Accounts = append(p.Accounts, phoneAccount{ID: id, Name: name})
		if id == p.Username {
			p.ApprovedName = name
		}
	}
	s.mu.Unlock()
	sort.Slice(p.Accounts, func(i, j int) bool { return p.Accounts[i].ID < p.Accounts[j].ID })
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = phoneTmpl.Execute(w, p)
}

func (s *Server) phoneApprove(w http.ResponseWriter, r *http.Request) {
	qr, acct := r.PostFormValue("qr"), r.PostFormValue("account")
	if err := s.ApproveQR(qr, acct); err != nil {
		s.renderPhone(w, 404, phonePage{Error: "This code expired or is not valid. Show a new code on your computer.", QRs: s.pendingQRs(), Username: acct})
		return
	}
	http.Redirect(w, r, "/phone?approved="+url.QueryEscape(qr)+"&account="+url.QueryEscape(acct), http.StatusSeeOther)
}
