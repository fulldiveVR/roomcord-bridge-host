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

// Package login maps bridgev2 login processes onto the wire login protocol and
// runs them (the small step manager that upstream keeps inside its appservice
// provisioning API).
package login

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

// retrySuffixes mark upstream steps that re-ask the same input after a wrong
// answer (Telegram uses ".incorrect" and ".invalid"). On the wire they become the
// base step with error set.
var retrySuffixes = []string{".incorrect", ".invalid"}

// MapStep converts an upstream login step into a wire step. loginKey turns an
// upstream user login id into the wire loginId.
func MapStep(ls *bridgev2.LoginStep, loginKey func(string) string) wire.Step {
	out := wire.Step{StepID: ls.StepID, Instructions: ls.Instructions}
	switch ls.Type {
	case bridgev2.LoginStepTypeDisplayAndWait:
		p := ls.DisplayAndWaitParams
		if p == nil {
			return unsupported(ls, "display_and_wait step without params")
		}
		out.Pollable = true
		switch p.Type {
		case bridgev2.LoginDisplayTypeQR:
			out.Kind = wire.KindQR
			out.Data = p.Data
		case bridgev2.LoginDisplayTypeCode:
			out.Kind = wire.KindPairingCode
			out.Code = p.Data
		case bridgev2.LoginDisplayTypeEmoji:
			out.Kind = wire.KindExternalApprovalWait
			out.Emoji = p.Data
			out.ImageURL = p.ImageURL
		case bridgev2.LoginDisplayTypeNothing:
			out.Kind = wire.KindExternalApprovalWait
			out.ImageURL = p.ImageURL
		default:
			return unsupported(ls, "unknown display type "+string(p.Type))
		}
	case bridgev2.LoginStepTypeUserInput:
		p := ls.UserInputParams
		if p == nil || len(p.Fields) == 0 {
			return unsupported(ls, "user_input step without fields")
		}
		if len(p.Fields) == 1 && len(p.Attachments) == 0 && !p.Fields[0].Optional {
			f := p.Fields[0]
			switch f.Type {
			case bridgev2.LoginInputFieldTypePhoneNumber:
				out.Kind, out.FieldID = wire.KindPhoneNumberInput, f.ID
			case bridgev2.LoginInputFieldType2FACode:
				out.Kind, out.FieldID = wire.KindCodeInput, f.ID
			case bridgev2.LoginInputFieldTypePassword:
				out.Kind, out.FieldID = wire.KindPasswordInput, f.ID
			}
		}
		if out.Kind == "" {
			out.Kind = wire.KindFormInput
			fields := make([]wire.FormField, len(p.Fields))
			for i, f := range p.Fields {
				fields[i] = wire.FormField{
					ID: f.ID, Type: string(f.Type), Name: f.Name, Description: f.Description,
					Pattern: f.Pattern, Options: f.Options, DefaultValue: f.DefaultValue, Placeholder: f.Placeholder,
					AutofillDomain: f.AutofillDomain, Optional: f.Optional, MinLength: f.MinLength, MaxLength: f.MaxLength,
				}
			}
			out.Fields = fields
			for _, a := range p.Attachments {
				if a == nil {
					continue
				}
				out.Attachments = append(out.Attachments, wire.FormAttachment{
					Type: string(a.Type), FileName: a.FileName, MimeType: a.Info.MimeType,
					Width: a.Info.Width, Height: a.Info.Height, DataBase64: base64.StdEncoding.EncodeToString(a.Content),
				})
			}
		}
		applyRetryRule(&out, p.Fields)
	case bridgev2.LoginStepTypeCookies:
		p := ls.CookiesParams
		if p == nil {
			return unsupported(ls, "cookies step without params")
		}
		out.Kind = wire.KindWebSessionCapture
		out.URL = p.URL
		out.UserAgent = p.UserAgent
		out.ExtractJS = p.ExtractJS
		out.WaitForURLPattern = p.WaitForURLPattern
		out.Hidden = p.Hidden
		out.JSRequired = p.JSRequired
		for _, c := range p.InitialCookies {
			out.InitialCookies = append(out.InitialCookies, wire.InitialCookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly,
			})
		}
		fields := make([]wire.CookieField, len(p.Fields))
		for i, f := range p.Fields {
			sources := make([]wire.CookieSource, len(f.Sources))
			onlySpecial := len(f.Sources) > 0
			for j, s := range f.Sources {
				if s.Type != bridgev2.LoginCookieTypeSpecial {
					onlySpecial = false
				}
				sources[j] = wire.CookieSource{Type: string(s.Type), Name: s.Name, CookieDomain: s.CookieDomain, RequestURLRegex: s.RequestURLRegex}
			}
			if f.Required && onlySpecial {
				out.JSRequired = true
			}
			fields[i] = wire.CookieField{ID: f.ID, Required: f.Required, Sources: sources, Pattern: f.Pattern}
		}
		out.Fields = fields
	case bridgev2.LoginStepTypeComplete:
		out.Kind = wire.KindDone
		if p := ls.CompleteParams; p != nil {
			out.RemoteID = string(p.UserLoginID)
			out.LoginID = loginKey(string(p.UserLoginID))
			if p.UserLogin != nil {
				out.RemoteName = p.UserLogin.RemoteName
			}
		}
	default:
		return unsupported(ls, "login step type "+string(ls.Type)+" is not supported by the bridge host")
	}
	return out
}

func unsupported(ls *bridgev2.LoginStep, msg string) wire.Step {
	return wire.Step{
		StepID: ls.StepID,
		Kind:   wire.KindFailed,
		Error:  &wire.StepError{Code: "unsupported_method", Message: msg, UpstreamCode: string(ls.Type)},
	}
}

func applyRetryRule(out *wire.Step, fields []bridgev2.LoginInputDataField) {
	for _, suffix := range retrySuffixes {
		base, ok := strings.CutSuffix(out.StepID, suffix)
		if !ok {
			continue
		}
		code := "invalid_input"
		if len(fields) == 1 {
			switch fields[0].Type {
			case bridgev2.LoginInputFieldTypePhoneNumber:
				code = "invalid_phone_number"
			case bridgev2.LoginInputFieldType2FACode:
				code = "invalid_code"
			case bridgev2.LoginInputFieldTypePassword:
				code = "invalid_password"
			}
		}
		msg := out.Instructions
		if msg == "" {
			msg = "The input was rejected"
		}
		out.StepID = base
		out.Error = &wire.StepError{Code: code, Message: msg, UpstreamCode: suffix[1:]}
		return
	}
}

// MapError converts an upstream login error into a failed wire step.
func MapError(err error) wire.Step {
	code, upstream := ClassifyError(err)
	return wire.Step{
		StepID: "failed",
		Kind:   wire.KindFailed,
		Error:  &wire.StepError{Code: code, Message: err.Error(), UpstreamCode: upstream},
	}
}

func ClassifyError(err error) (code, upstream string) {
	var re mautrix.RespError
	if errors.As(err, &re) {
		upstream = re.ErrCode
	}
	u := strings.ToUpper(upstream)
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled", upstream
	case errors.Is(err, context.DeadlineExceeded):
		return "expired", upstream
	case upstream == "":
		return "internal_error", upstream
	case strings.Contains(u, "RATE") || strings.Contains(u, "FLOOD") || strings.Contains(u, "LIMIT_EXCEEDED"):
		return "rate_limited", upstream
	case strings.Contains(u, "TIMEOUT") || strings.Contains(u, "TIMED_OUT"):
		return "expired", upstream
	case strings.Contains(u, "CANCELLED"):
		return "cancelled", upstream
	case strings.Contains(u, "PHONE_NUMBER"):
		return "invalid_phone_number", upstream
	case strings.Contains(u, "PHONE_CODE") || strings.Contains(u, "CODE_INVALID"):
		return "invalid_code", upstream
	case strings.Contains(u, "PASSWORD"):
		return "invalid_password", upstream
	case strings.Contains(u, "WEBAUTHN") || strings.Contains(u, "NOT_SUPPORTED") || strings.Contains(u, "NOT_IMPLEMENTED"):
		return "unsupported_method", upstream
	case strings.Contains(u, "API_ID") || strings.Contains(u, "API_HASH"):
		return "internal_error", upstream
	default:
		return "internal_error", upstream
	}
}
