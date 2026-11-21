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

package login

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
)

func captureParams() *bridgev2.LoginCookiesParams {
	src := func(t bridgev2.LoginCookieFieldSourceType, name string) bridgev2.LoginCookieFieldSource {
		return bridgev2.LoginCookieFieldSource{Type: t, Name: name}
	}
	return &bridgev2.LoginCookiesParams{
		URL: "https://example.com/login",
		Fields: []bridgev2.LoginCookieField{
			{ID: "session", Required: true, Pattern: "^sess-[0-9]+", Sources: []bridgev2.LoginCookieFieldSource{src(bridgev2.LoginCookieTypeCookie, "sid")}},
			{ID: "track", Required: true, Sources: []bridgev2.LoginCookieFieldSource{src(bridgev2.LoginCookieTypeRequestHeader, "X-Track")}},
			{ID: "body", Sources: []bridgev2.LoginCookieFieldSource{src(bridgev2.LoginCookieTypeRequestBody, "nonce")}},
			{ID: "ua", Sources: []bridgev2.LoginCookieFieldSource{src(bridgev2.LoginCookieTypeSpecial, "ua")}},
		},
	}
}

func withStorageField(p *bridgev2.LoginCookiesParams) *bridgev2.LoginCookiesParams {
	p.Fields = append(p.Fields, bridgev2.LoginCookieField{ID: "token", Required: true,
		Sources: []bridgev2.LoginCookieFieldSource{{Type: bridgev2.LoginCookieTypeLocalStorage, Name: "tok"}}})
	return p
}

const secretValue = "sess-12345-SECRET"

func TestCaptureInputMap(t *testing.T) {
	good := func() map[string]string { return map[string]string{"session": secretValue, "track": "tr-SECRET"} }
	tests := []struct {
		name    string
		mutate  func(map[string]string) map[string]string
		wantErr string
	}{
		{"required only", func(m map[string]string) map[string]string { return m }, ""},
		{"with optional", func(m map[string]string) map[string]string { m["body"] = "n"; return m }, ""},
		{"empty optional is dropped", func(m map[string]string) map[string]string { m["ua"] = ""; return m }, ""},
		{"unknown id", func(m map[string]string) map[string]string { m["other-SECRET"] = "x"; return m }, "unknown field"},
		{"missing required", func(m map[string]string) map[string]string { delete(m, "track"); return m }, "track"},
		{"empty required", func(m map[string]string) map[string]string { m["track"] = ""; return m }, "track"},
		{"nil map", func(map[string]string) map[string]string { return nil }, "session"},
		{"pattern mismatch", func(m map[string]string) map[string]string { m["session"] = "prelogin-SECRET"; return m }, "session"},
		{"over 64 KB", func(m map[string]string) map[string]string { m["body"] = strings.Repeat("a", 64*1024); return m }, "too large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.mutate(good())
			out, serr := captureInput(captureParams(), in, "")
			if tc.wantErr == "" {
				if serr != nil {
					t.Fatalf("unexpected error %+v", serr)
				}
				if out["session"] != secretValue || out["track"] != "tr-SECRET" {
					t.Fatalf("values changed: %d fields", len(out))
				}
				if _, ok := out["ua"]; ok {
					t.Fatal("empty optional value was kept")
				}
				return
			}
			if serr == nil || serr.Code != "invalid_input" || out != nil {
				t.Fatalf("got %+v, want invalid_input", serr)
			}
			if !strings.Contains(serr.Message, tc.wantErr) {
				t.Errorf("message %q does not name %q", serr.Message, tc.wantErr)
			}
			if strings.Contains(serr.Message, "SECRET") {
				t.Errorf("message leaks a submitted value: %q", serr.Message)
			}
		})
	}
}

func TestCaptureInputSizeBoundary(t *testing.T) {
	p := captureParams()
	base := map[string]string{"session": "sess-1", "track": "t"}
	used := 0
	for k, v := range base {
		used += len(k) + len(v)
	}
	base["body"] = strings.Repeat("a", maxCaptureBytes-used-len("body"))
	if _, serr := captureInput(p, base, ""); serr != nil {
		t.Fatalf("exactly 64 KB was rejected: %+v", serr)
	}
	base["body"] += "a"
	if _, serr := captureInput(p, base, ""); serr == nil {
		t.Fatal("64 KB + 1 was accepted")
	}
}

func TestCaptureInputPaste(t *testing.T) {
	tests := []struct {
		name    string
		params  *bridgev2.LoginCookiesParams
		paste   string
		want    map[string]string
		wantErr string
	}{
		{"json object", captureParams(), ` {"session": "sess-1%3Dx", "track": "tr 1", "body": "n"} `,
			map[string]string{"session": "sess-1=x", "track": "tr 1", "body": "n"}, ""},
		{"json with a local-storage field", withStorageField(captureParams()), `{"session":"sess-1","track":"t","token":"tok-1"}`,
			map[string]string{"session": "sess-1", "track": "t", "token": "tok-1"}, ""},
		{"json unknown id", captureParams(), `{"session":"sess-1","track":"t","zzz":"1"}`, nil, "unknown field"},
		{"json missing required", captureParams(), `{"session":"sess-1"}`, nil, "track"},
		{"json pattern mismatch", captureParams(), `{"session":"nope-SECRET","track":"t"}`, nil, "session"},
		{"json array", captureParams(), `["sess-1-SECRET"]`, nil, "JSON object"},
		{"json with a non-string value", captureParams(), `{"session":{"a":"SECRET"},"track":"t"}`, nil, "JSON object"},
		{"curl with a Cookie header", captureParams(),
			`curl 'https://example.com/api/x' -H 'accept: */*' -H 'Cookie: a=1; sid=sess-7%3Dz; b=2' -H 'x-track: tr-7'`,
			map[string]string{"session": "sess-7=z", "track": "tr-7"}, ""},
		{"curl with -b", captureParams(),
			"curl 'https://example.com/api/x' \\\n  -b 'a=1; sid=sess-8' \\\n  -H 'X-Track: tr-8'",
			map[string]string{"session": "sess-8", "track": "tr-8"}, ""},
		{"curl with a JSON body", captureParams(),
			`curl 'https://example.com/api/x' -X POST -H 'content-type: application/json' -b 'sid=sess-9' -H 'X-Track: tr-9' --data-raw '{"nonce":"n-9","other":1}'`,
			map[string]string{"session": "sess-9", "track": "tr-9", "body": "n-9"}, ""},
		{"curl without the cookie", captureParams(), `curl 'https://example.com/' -H 'X-Track: tr-SECRET'`, nil, "session"},
		{"curl with a wrong cookie value", captureParams(), `curl 'https://example.com/' -b 'sid=bad-SECRET' -H 'X-Track: t'`, nil, "session"},
		{"curl cannot give a local-storage field", withStorageField(captureParams()),
			`curl 'https://example.com/' -b 'sid=sess-1' -H 'X-Track: t'`, nil, "JSON object"},
		{"curl cut off after -H", captureParams(), `curl 'https://example.com/' -b 'sid=sess-SECRET' -H`, nil, "cURL"},
		{"curl header without a colon", captureParams(), `curl 'https://example.com/' -H 'SECRET'`, nil, "cURL"},
		{"curl with an open quote", captureParams(), `curl 'https://example.com/ -b sid=sess-SECRET`, nil, "cURL"},
		{"junk", captureParams(), `hello SECRET world`, nil, "JSON object"},
		{"empty", captureParams(), `   `, nil, "empty"},
		{"too large", captureParams(), `{"session":"sess-1","track":"` + strings.Repeat("S", 64*1024) + `"}`, nil, "too large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, serr := captureInput(tc.params, nil, tc.paste)
			if tc.wantErr == "" {
				if serr != nil {
					t.Fatalf("unexpected error %+v", serr)
				}
				if len(out) != len(tc.want) {
					t.Fatalf("got %d fields, want %d", len(out), len(tc.want))
				}
				for k, v := range tc.want {
					if out[k] != v {
						t.Errorf("field %s = %q, want %q", k, out[k], v)
					}
				}
				return
			}
			if serr == nil || serr.Code != "invalid_input" || out != nil {
				t.Fatalf("got %+v, want invalid_input", serr)
			}
			if !strings.Contains(serr.Message, tc.wantErr) {
				t.Errorf("message %q does not contain %q", serr.Message, tc.wantErr)
			}
			if strings.Contains(serr.Message, "SECRET") {
				t.Errorf("message leaks pasted content: %q", serr.Message)
			}
		})
	}
}

func TestCaptureInputPasteAndMap(t *testing.T) {
	_, serr := captureInput(captureParams(), map[string]string{"session": "sess-1", "track": "t"}, `{"session":"sess-1","track":"t"}`)
	if serr == nil || serr.Code != "invalid_input" {
		t.Fatalf("got %+v, want invalid_input", serr)
	}
}
