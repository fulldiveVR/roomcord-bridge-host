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
	"fmt"
	"net"
	"net/http"
	"regexp"
)

// ProviderPath is the root of the fake provider pages (PROTOCOL.md, "Fake network").
const ProviderPath = "/fake-provider"

var accountRe = regexp.MustCompile("^[0-9]{1,18}$")

// ProviderRoutes returns the fake provider: a tiny sign-in site that the capture
// step of flow `web` opens. It needs no bearer token, because a browser or a web
// view loads it, and it answers loopback clients only.
func ProviderRoutes() map[string]http.Handler {
	return map[string]http.Handler{
		"GET " + ProviderPath + "/login":     loopbackOnly(providerLoginPage),
		"POST " + ProviderPath + "/login":    loopbackOnly(providerSignIn),
		"GET " + ProviderPath + "/home":      loopbackOnly(providerHome),
		"POST " + ProviderPath + "/api/ping": loopbackOnly(providerPing),
	}
}

func loopbackOnly(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "the fake provider answers loopback clients only", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	})
}

const providerLoginHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Fake provider: sign in</title></head>
<body>
<h1>Fake provider</h1>
<p>A test page of roomcord-bridge-host. It is not a real service.</p>
<p id="initial">initial cookie: %s</p>
<p id="error">%s</p>
<form method="post" action="/fake-provider/login">
<label>Account number <input name="account" value="1001" inputmode="numeric" pattern="[0-9]{1,18}" required></label>
<button type="submit" id="sign-in">Sign in</button>
</form>
</body>
</html>
`

const providerHomeHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Fake provider: home</title></head>
<body>
<h1>Fake provider</h1>
<p id="status">signed in as account %[1]s</p>
<p id="ping">ping pending</p>
<script>
localStorage.setItem("fake_token", "fake-token-%[1]s");
fetch("/fake-provider/api/ping", {
  method: "POST",
  headers: { "Content-Type": "application/json", "X-Fake-Track": "fake-track-%[1]s" },
  body: JSON.stringify({ fake_body: "fake-body-%[1]s" }),
}).then(function (res) {
  document.getElementById("ping").textContent = res.ok ? "ping ok" : "ping failed";
});
</script>
</body>
</html>
`

func providerLoginPage(w http.ResponseWriter, r *http.Request) {
	initial := "absent"
	if c, err := r.Cookie(WebInitialCookieName); err == nil && c.Value == WebInitialCookie {
		initial = "present"
	}
	msg := ""
	if r.URL.Query().Get("error") == "account" {
		msg = "The account number must have 1 to 18 digits."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, providerLoginHTML, initial, msg)
}

func providerSignIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	account := r.PostFormValue("account")
	if !accountRe.MatchString(account) {
		http.Redirect(w, r, ProviderPath+"/login?error=account", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: WebCookieName, Value: "fake-session-" + account, Path: ProviderPath, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, ProviderPath+"/home", http.StatusSeeOther)
}

func providerHome(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(WebCookieName)
	var m []string
	if err == nil {
		m = webSessionRe.FindStringSubmatch(c.Value)
	}
	if m == nil {
		http.Redirect(w, r, ProviderPath+"/login", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, providerHomeHTML, m[1])
}

func providerPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get(WebHeaderName) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false}` + "\n"))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}` + "\n"))
}
