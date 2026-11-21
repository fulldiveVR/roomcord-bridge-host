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
	"fmt"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

// maxCaptureBytes caps one capture submit: the paste text, or all field ids and values together.
const maxCaptureBytes = 64 * 1024

func captureError(format string, args ...any) *wire.StepError {
	return &wire.StepError{Code: "invalid_input", Message: fmt.Sprintf(format, args...)}
}

// captureInput turns a capture submit into the map for SubmitCookies. The error
// messages name field ids of the step only, never a submitted id, value or paste text.
func captureInput(p *bridgev2.LoginCookiesParams, values map[string]string, paste string) (map[string]string, *wire.StepError) {
	if paste != "" {
		if len(values) > 0 {
			return nil, captureError("send either cookies or paste, not both")
		}
		parsed, serr := parsePaste(p, paste)
		if serr != nil {
			return nil, serr
		}
		values = parsed
	}
	return validateCapture(p, values)
}

func validateCapture(p *bridgev2.LoginCookiesParams, values map[string]string) (map[string]string, *wire.StepError) {
	size := 0
	for id, val := range values {
		size += len(id) + len(val)
	}
	if size > maxCaptureBytes {
		return nil, captureError("the captured values are too large (limit %d bytes)", maxCaptureBytes)
	}
	out := make(map[string]string, len(values))
	var missing []string
	for _, f := range p.Fields {
		val := values[f.ID]
		if val == "" {
			if f.Required {
				missing = append(missing, f.ID)
			}
			continue
		}
		if f.Pattern != "" {
			// A pattern that Go cannot compile is skipped: it must not block every login.
			if re, err := regexp.Compile(f.Pattern); err == nil && !re.MatchString(val) {
				return nil, captureError("the value of %s does not match its pattern", f.ID)
			}
		}
		out[f.ID] = val
	}
	unknown := 0
	for id := range values {
		if _, ok := out[id]; !ok && !hasField(p, id) {
			unknown++
		}
	}
	if unknown > 0 {
		return nil, captureError("%d unknown field id(s) in the capture", unknown)
	}
	if len(missing) > 0 {
		return nil, captureError("missing required fields: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func hasField(p *bridgev2.LoginCookiesParams, id string) bool {
	for _, f := range p.Fields {
		if f.ID == id {
			return true
		}
	}
	return false
}
