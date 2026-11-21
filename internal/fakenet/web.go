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
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
)

// Flow `web`: a browser-session capture against the fake provider pages (provider.go).
const (
	StepWeb = "fake.login.web"
	// DefaultCaptureURL is the page of the capture step when nothing else is configured.
	DefaultCaptureURL = "http://127.0.0.1:29391" + ProviderPath + "/login"

	WebCookieName        = "session"
	WebStorageKey        = "fake_token"
	WebHeaderName        = "X-Fake-Track"
	WebBodyField         = "fake_body"
	WebSpecialName       = "fake.ua"
	WebInitialCookieName = "fake_initial"
	WebInitialCookie     = "fake-initial-1"
	WebSessionPattern    = "^fake-session-[0-9]+$"
	WebWaitPattern       = "/fake-provider/home$"
	WebExtractJS         = "new Promise((resolve) => resolve({ ua: navigator.userAgent }))"
	// WebRejectedAccount is the account number whose session the fake network refuses.
	WebRejectedAccount = "0"
)

var webSessionRe = regexp.MustCompile("^fake-session-([0-9]+)$")

// SetCaptureURL sets the page that the capture step of flow `web` opens. An empty
// value restores DefaultCaptureURL.
func (fc *Connector) SetCaptureURL(raw string) {
	if raw == "" {
		raw = DefaultCaptureURL
	}
	fc.mu.Lock()
	fc.captureURL = raw
	fc.mu.Unlock()
}

func (fc *Connector) CaptureURL() string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.captureURL == "" {
		return DefaultCaptureURL
	}
	return fc.captureURL
}

type webLogin struct {
	main *Connector
	user *bridgev2.User
}

var _ bridgev2.LoginProcessCookies = (*webLogin)(nil)

func (l *webLogin) Cancel() {}

// Start asks for one value per source type. The cookie domain and the request URL
// regex follow the origin of the capture URL.
func (l *webLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	raw := l.main.CaptureURL()
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the fake capture URL is not a valid URL")
	}
	apiRegex := "^" + regexp.QuoteMeta(u.Scheme+"://"+u.Host) + "/fake-provider/api/"
	field := func(id string, required bool, pattern string, src bridgev2.LoginCookieFieldSource) bridgev2.LoginCookieField {
		return bridgev2.LoginCookieField{ID: id, Required: required, Pattern: pattern, Sources: []bridgev2.LoginCookieFieldSource{src}}
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       StepWeb,
		Instructions: "Sign in on the fake provider page.",
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL: raw,
			InitialCookies: []bridgev2.LoginCookie{{
				Name: WebInitialCookieName, Value: WebInitialCookie, Domain: u.Hostname(), Path: ProviderPath, HTTPOnly: true,
			}},
			Fields: []bridgev2.LoginCookieField{
				field("session", true, WebSessionPattern, bridgev2.LoginCookieFieldSource{
					Type: bridgev2.LoginCookieTypeCookie, Name: WebCookieName, CookieDomain: u.Hostname()}),
				field("token", true, "", bridgev2.LoginCookieFieldSource{
					Type: bridgev2.LoginCookieTypeLocalStorage, Name: WebStorageKey}),
				field("track", true, "", bridgev2.LoginCookieFieldSource{
					Type: bridgev2.LoginCookieTypeRequestHeader, Name: WebHeaderName, RequestURLRegex: apiRegex}),
				field("body", true, "", bridgev2.LoginCookieFieldSource{
					Type: bridgev2.LoginCookieTypeRequestBody, Name: WebBodyField, RequestURLRegex: apiRegex}),
				field("ua", false, "", bridgev2.LoginCookieFieldSource{
					Type: bridgev2.LoginCookieTypeSpecial, Name: WebSpecialName}),
			},
			ExtractJS:         WebExtractJS,
			WaitForURLPattern: WebWaitPattern,
		},
	}, nil
}

func (l *webLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	m := webSessionRe.FindStringSubmatch(cookies["session"])
	if m == nil {
		return nil, errors.New("the fake provider session is not valid")
	}
	for _, id := range []string{"token", "track", "body"} {
		if cookies[id] == "" {
			return nil, fmt.Errorf("the fake provider value %s is missing", id)
		}
	}
	if m[1] == WebRejectedAccount {
		// Like an upstream connector whose error quotes its input: the host must not log it.
		return nil, fmt.Errorf("the fake provider refused session %q", cookies["session"])
	}
	name := "web-" + m[1]
	ul, err := l.user.NewLogin(ctx, &database.UserLogin{
		ID: networkid.UserLoginID(name), RemoteName: name, RemoteProfile: status.RemoteProfile{Name: "Fake " + name},
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, err
	}
	ul.Client.Connect(ul.Log.WithContext(l.main.Bridge.BackgroundCtx))
	return &bridgev2.LoginStep{
		Type: bridgev2.LoginStepTypeComplete, StepID: StepComplete, Instructions: "Logged in as " + name,
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}
