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
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"go.mau.fi/util/curl"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

// parsePaste reads a pasted JSON object {fieldId: value} or a "Copy as cURL" command,
// with the rules of the upstream bot login command (bridgev2/commands/login.go).
func parsePaste(p *bridgev2.LoginCookiesParams, paste string) (map[string]string, *wire.StepError) {
	if len(paste) > maxCaptureBytes {
		return nil, captureError("the paste is too large (limit %d bytes)", maxCaptureBytes)
	}
	paste = strings.TrimSpace(paste)
	if paste == "" {
		return nil, captureError("the paste is empty")
	}
	if strings.HasPrefix(paste, "curl") {
		return parseCurlPaste(p, paste)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(paste), &values); err != nil || values == nil {
		return nil, captureError("the paste is neither a cURL command nor a JSON object of text values")
	}
	for _, f := range p.Fields {
		if val, ok := values[f.ID]; ok && val != "" && hasSource(f, bridgev2.LoginCookieTypeCookie) {
			if decoded, err := url.PathUnescape(val); err == nil {
				values[f.ID] = decoded
			}
		}
	}
	return values, nil
}

func hasSource(f bridgev2.LoginCookieField, t bridgev2.LoginCookieFieldSourceType) bool {
	for _, s := range f.Sources {
		if s.Type == t {
			return true
		}
	}
	return false
}

// safeCurlParse hides the parser's error text (it can quote the command) and its
// panics on a command that ends after a flag or has a header without ": ".
func safeCurlParse(cmd string) (parsed *curl.Parsed, ok bool) {
	defer func() {
		if recover() != nil {
			parsed, ok = nil, false
		}
	}()
	parsed, err := curl.Parse(cmd)
	return parsed, err == nil && parsed != nil
}

func parseCurlPaste(p *bridgev2.LoginCookiesParams, cmd string) (map[string]string, *wire.StepError) {
	parsed, ok := safeCurlParse(cmd)
	if !ok {
		return nil, captureError("the pasted cURL command could not be parsed")
	}
	cookies := make(map[string]string)
	for _, c := range parsed.Cookies() {
		val, err := url.PathUnescape(c.Value)
		if err != nil {
			return nil, captureError("a cookie of the pasted cURL command could not be decoded")
		}
		cookies[c.Name] = val
	}
	values := make(map[string]string)
	var missing, unsupported []string
	for _, f := range p.Fields {
		var value string
		var supported bool
		for _, src := range f.Sources {
			switch src.Type {
			case bridgev2.LoginCookieTypeCookie:
				supported = true
				value = cookies[src.Name]
			case bridgev2.LoginCookieTypeRequestHeader:
				supported = true
				value = parsed.Header.Get(src.Name)
			case bridgev2.LoginCookieTypeRequestBody:
				supported = true
				switch {
				case parsed.MultipartForm != nil:
					if vals := parsed.MultipartForm.Value[src.Name]; len(vals) > 0 {
						value = vals[0]
					}
				case parsed.ParsedJSON != nil:
					if raw, ok := parsed.ParsedJSON[src.Name]; ok {
						value = fmt.Sprintf("%v", raw)
					}
				}
			}
			if value != "" {
				values[f.ID] = value
				break
			}
		}
		if value == "" && f.Required {
			if supported {
				missing = append(missing, f.ID)
			} else {
				unsupported = append(unsupported, f.ID)
			}
		}
	}
	if len(unsupported) > 0 {
		return nil, captureError("a cURL command cannot carry these fields: %s. Paste a JSON object instead", strings.Join(unsupported, ", "))
	} else if len(missing) > 0 {
		return nil, captureError("the pasted cURL command lacks these fields: %s", strings.Join(missing, ", "))
	}
	return values, nil
}
