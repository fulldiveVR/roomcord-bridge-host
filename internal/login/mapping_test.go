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
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

func key(id string) string { return "net:" + id }

func input(stepID string, fields ...bridgev2.LoginInputDataField) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type: bridgev2.LoginStepTypeUserInput, StepID: stepID, Instructions: "do it",
		UserInputParams: &bridgev2.LoginUserInputParams{Fields: fields},
	}
}

func field(t bridgev2.LoginInputFieldType, id string) bridgev2.LoginInputDataField {
	return bridgev2.LoginInputDataField{Type: t, ID: id, Name: "N " + id, Description: "D " + id}
}

func display(t bridgev2.LoginDisplayType, data string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type: bridgev2.LoginStepTypeDisplayAndWait, StepID: "s.display",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: t, Data: data},
	}
}

func TestMapStep(t *testing.T) {
	tests := []struct {
		name string
		in   *bridgev2.LoginStep
		want wire.Step
	}{
		{"qr", display(bridgev2.LoginDisplayTypeQR, "2@abc"),
			wire.Step{StepID: "s.display", Kind: wire.KindQR, Pollable: true, Data: "2@abc"}},
		{"pairing code", display(bridgev2.LoginDisplayTypeCode, "ABCD-EFGH"),
			wire.Step{StepID: "s.display", Kind: wire.KindPairingCode, Pollable: true, Code: "ABCD-EFGH"}},
		{"emoji", display(bridgev2.LoginDisplayTypeEmoji, "🐱"),
			wire.Step{StepID: "s.display", Kind: wire.KindExternalApprovalWait, Pollable: true, Emoji: "🐱"}},
		{"nothing", display(bridgev2.LoginDisplayTypeNothing, ""),
			wire.Step{StepID: "s.display", Kind: wire.KindExternalApprovalWait, Pollable: true}},
		{"phone", input("s.phone", field(bridgev2.LoginInputFieldTypePhoneNumber, "phone_number")),
			wire.Step{StepID: "s.phone", Kind: wire.KindPhoneNumberInput, Instructions: "do it", FieldID: "phone_number"}},
		{"code", input("s.code", field(bridgev2.LoginInputFieldType2FACode, "code")),
			wire.Step{StepID: "s.code", Kind: wire.KindCodeInput, Instructions: "do it", FieldID: "code"}},
		{"password", input("s.pw", field(bridgev2.LoginInputFieldTypePassword, "pw")),
			wire.Step{StepID: "s.pw", Kind: wire.KindPasswordInput, Instructions: "do it", FieldID: "pw"}},
		{"single email is a form", input("s.email", field(bridgev2.LoginInputFieldTypeEmail, "email")),
			wire.Step{StepID: "s.email", Kind: wire.KindFormInput, Instructions: "do it", Fields: []wire.FormField{
				{ID: "email", Type: "email", Name: "N email", Description: "D email"}}}},
		{"multi field form", input("s.form",
			field(bridgev2.LoginInputFieldTypeUsername, "user"),
			bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypeSelect, ID: "dc", Name: "DC", Options: []string{"a", "b"}, Pattern: "^(a|b)$"}),
			wire.Step{StepID: "s.form", Kind: wire.KindFormInput, Instructions: "do it", Fields: []wire.FormField{
				{ID: "user", Type: "username", Name: "N user", Description: "D user"},
				{ID: "dc", Type: "select", Name: "DC", Pattern: "^(a|b)$", Options: []string{"a", "b"}}}}},
		{"single token field is a form", input("s.tok", field(bridgev2.LoginInputFieldTypeToken, "token")),
			wire.Step{StepID: "s.tok", Kind: wire.KindFormInput, Instructions: "do it", Fields: []wire.FormField{
				{ID: "token", Type: "token", Name: "N token", Description: "D token"}}}},
		{"cookies", &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeCookies, StepID: "s.cookies",
			CookiesParams: &bridgev2.LoginCookiesParams{
				URL: "https://example.com/login", UserAgent: "UA", ExtractJS: "js()", WaitForURLPattern: "^https://example.com/home",
				Fields: []bridgev2.LoginCookieField{{ID: "sid", Required: true, Sources: []bridgev2.LoginCookieFieldSource{
					{Type: bridgev2.LoginCookieTypeCookie, Name: "sid", CookieDomain: "example.com"},
					{Type: bridgev2.LoginCookieTypeRequestHeader, Name: "X-Sid", RequestURLRegex: "api"},
				}}},
			}},
			wire.Step{StepID: "s.cookies", Kind: wire.KindWebSessionCapture, URL: "https://example.com/login", UserAgent: "UA",
				ExtractJS: "js()", JSRequired: true, WaitForURLPattern: "^https://example.com/home", Fields: []wire.CookieField{{ID: "sid", Required: true, Sources: []wire.CookieSource{
					{Type: "cookie", Name: "sid", CookieDomain: "example.com"},
					{Type: "request_header", Name: "X-Sid", RequestURLRegex: "api"},
				}}}}},
		{"emoji with image", &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeDisplayAndWait, StepID: "s.emoji",
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeEmoji, Data: "🦊", ImageURL: "https://example.com/fox.png"}},
			wire.Step{StepID: "s.emoji", Kind: wire.KindExternalApprovalWait, Pollable: true, Emoji: "🦊", ImageURL: "https://example.com/fox.png"}},
		{"single select is a form", input("s.sel", bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypeSelect, ID: "ws", Name: "Workspace", Options: []string{"a", "b"}}),
			wire.Step{StepID: "s.sel", Kind: wire.KindFormInput, Instructions: "do it", Fields: []wire.FormField{
				{ID: "ws", Type: "select", Name: "Workspace", Options: []string{"a", "b"}}}}},
		{"optional single password is a form", input("s.pin", bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypePassword, ID: "pin", Name: "PIN", Optional: true, MinLength: 6, MaxLength: 6, Pattern: "^[0-9]{6}$"}),
			wire.Step{StepID: "s.pin", Kind: wire.KindFormInput, Instructions: "do it", Fields: []wire.FormField{
				{ID: "pin", Type: "password", Name: "PIN", Pattern: "^[0-9]{6}$", Optional: true, MinLength: 6, MaxLength: 6}}}},
		{"field extras", input("s.x", bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypeUsername, ID: "u", Name: "U", DefaultValue: "d", Placeholder: "p", AutofillDomain: "example.com"},
			field(bridgev2.LoginInputFieldTypePassword, "pw")),
			wire.Step{StepID: "s.x", Kind: wire.KindFormInput, Instructions: "do it", Fields: []wire.FormField{
				{ID: "u", Type: "username", Name: "U", DefaultValue: "d", Placeholder: "p", AutofillDomain: "example.com"},
				{ID: "pw", Type: "password", Name: "N pw", Description: "D pw"}}}},
		{"captcha with image is a form with attachments", &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeUserInput, StepID: "s.captcha",
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{{Type: bridgev2.LoginInputFieldTypeCaptchaCode, ID: "captcha", Name: "Captcha"}},
				Attachments: []*bridgev2.LoginUserInputAttachment{{Type: "m.image", FileName: "c.png", Content: []byte{1, 2, 3},
					Info: bridgev2.LoginUserInputAttachmentInfo{MimeType: "image/png", Width: 10, Height: 5}}},
			}},
			wire.Step{StepID: "s.captcha", Kind: wire.KindFormInput, Fields: []wire.FormField{{ID: "captcha", Type: "captcha_code", Name: "Captcha"}},
				Attachments: []wire.FormAttachment{{Type: "m.image", FileName: "c.png", MimeType: "image/png", Width: 10, Height: 5, DataBase64: "AQID"}}}},
		{"hidden special capture with initial cookies", &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeCookies, StepID: "s.hidden",
			CookiesParams: &bridgev2.LoginCookiesParams{
				URL: "https://example.com/robots.txt", Hidden: true,
				InitialCookies: []bridgev2.LoginCookie{{Name: "a", Value: "b", Domain: "example.com", Secure: true}},
				Fields: []bridgev2.LoginCookieField{{ID: "token", Required: true, Pattern: "^t", Sources: []bridgev2.LoginCookieFieldSource{
					{Type: bridgev2.LoginCookieTypeSpecial, Name: "castle_token"},
					{Type: bridgev2.LoginCookieTypeLocalStorage, Name: "tok"},
					{Type: bridgev2.LoginCookieTypeRequestBody, Name: "token", RequestURLRegex: "api"},
				}}},
			}},
			wire.Step{StepID: "s.hidden", Kind: wire.KindWebSessionCapture, URL: "https://example.com/robots.txt", Hidden: true, JSRequired: true,
				InitialCookies: []wire.InitialCookie{{Name: "a", Value: "b", Domain: "example.com", Secure: true}},
				Fields: []wire.CookieField{{ID: "token", Required: true, Pattern: "^t", Sources: []wire.CookieSource{
					{Type: "special", Name: "castle_token"}, {Type: "local_storage", Name: "tok"}, {Type: "request_body", Name: "token", RequestURLRegex: "api"},
				}}}}},
		{"js_required flag", &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeCookies, StepID: "s.js",
			CookiesParams: &bridgev2.LoginCookiesParams{URL: "https://example.com/", JSRequired: true,
				Fields: []bridgev2.LoginCookieField{{ID: "c", Sources: []bridgev2.LoginCookieFieldSource{{Type: bridgev2.LoginCookieTypeCookie, Name: "c"}}}}}},
			wire.Step{StepID: "s.js", Kind: wire.KindWebSessionCapture, URL: "https://example.com/", JSRequired: true,
				Fields: []wire.CookieField{{ID: "c", Sources: []wire.CookieSource{{Type: "cookie", Name: "c"}}}}}},
		{"unknown display type", display("hologram", "x"),
			wire.Step{StepID: "s.display", Kind: wire.KindFailed, Error: &wire.StepError{Code: "unsupported_method",
				Message: "unknown display type hologram", UpstreamCode: "display_and_wait"}}},
		{"complete", &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeComplete, StepID: "s.done",
			CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: "42"}},
			wire.Step{StepID: "s.done", Kind: wire.KindDone, LoginID: "net:42", RemoteID: "42"}},
		{"webauthn unsupported", &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeWebAuthn, StepID: "s.passkey"},
			wire.Step{StepID: "s.passkey", Kind: wire.KindFailed, Error: &wire.StepError{Code: "unsupported_method",
				Message: "login step type webauthn is not supported by the bridge host", UpstreamCode: "webauthn"}}},
		{"client_http unsupported", &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeClientHTTP, StepID: "s.http"},
			wire.Step{StepID: "s.http", Kind: wire.KindFailed, Error: &wire.StepError{Code: "unsupported_method",
				Message: "login step type client_http is not supported by the bridge host", UpstreamCode: "client_http"}}},
		{"retry code incorrect", input("fi.mau.telegram.login.code.incorrect", field(bridgev2.LoginInputFieldType2FACode, "code")),
			wire.Step{StepID: "fi.mau.telegram.login.code", Kind: wire.KindCodeInput, Instructions: "do it", FieldID: "code",
				Error: &wire.StepError{Code: "invalid_code", Message: "do it", UpstreamCode: "incorrect"}}},
		{"retry password incorrect", input("fi.mau.telegram.login.password.incorrect", field(bridgev2.LoginInputFieldTypePassword, "pw")),
			wire.Step{StepID: "fi.mau.telegram.login.password", Kind: wire.KindPasswordInput, Instructions: "do it", FieldID: "pw",
				Error: &wire.StepError{Code: "invalid_password", Message: "do it", UpstreamCode: "incorrect"}}},
		{"retry phone invalid", input("fi.mau.telegram.login.phone_number.invalid", field(bridgev2.LoginInputFieldTypePhoneNumber, "p")),
			wire.Step{StepID: "fi.mau.telegram.login.phone_number", Kind: wire.KindPhoneNumberInput, Instructions: "do it", FieldID: "p",
				Error: &wire.StepError{Code: "invalid_phone_number", Message: "do it", UpstreamCode: "invalid"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MapStep(tc.in, key)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MapStep()\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestMapError(t *testing.T) {
	tests := []struct {
		err      error
		code     string
		upstream string
	}{
		{bridgev2.RespError{ErrCode: "FI.MAU.WHATSAPP.LOGIN_TIMEOUT", Err: "timeout"}, "expired", "FI.MAU.WHATSAPP.LOGIN_TIMEOUT"},
		{bridgev2.RespError{ErrCode: "FI.MAU.WHATSAPP.RATE_LIMITED", Err: "slow"}, "rate_limited", "FI.MAU.WHATSAPP.RATE_LIMITED"},
		{bridgev2.RespError{ErrCode: "FI.MAU.WHATSAPP.PHONE_NUMBER_TOO_SHORT", Err: "short"}, "invalid_phone_number", "FI.MAU.WHATSAPP.PHONE_NUMBER_TOO_SHORT"},
		{bridgev2.RespError{ErrCode: "FI.MAU.TELEGRAM.PHONE_CODE_INVALID", Err: "bad"}, "invalid_code", "FI.MAU.TELEGRAM.PHONE_CODE_INVALID"},
		{bridgev2.RespError{ErrCode: "FI.MAU.TELEGRAM.INVALID_PASSWORD", Err: "bad"}, "invalid_password", "FI.MAU.TELEGRAM.INVALID_PASSWORD"},
		{bridgev2.RespError{ErrCode: "M_LIMIT_EXCEEDED", Err: "flood"}, "rate_limited", "M_LIMIT_EXCEEDED"},
		{fmt.Errorf("wrapped: %w", bridgev2.RespError{ErrCode: "FI.MAU.TELEGRAM.API_ID_INVALID", Err: "x"}), "internal_error", "FI.MAU.TELEGRAM.API_ID_INVALID"},
		{context.DeadlineExceeded, "expired", ""},
		{context.Canceled, "cancelled", ""},
		{errors.New("boom"), "internal_error", ""},
	}
	for _, tc := range tests {
		got := MapError(tc.err)
		if got.Kind != wire.KindFailed || got.Error == nil || got.Error.Code != tc.code || got.Error.UpstreamCode != tc.upstream {
			t.Errorf("MapError(%v) = %+v (err %+v), want code %s upstream %s", tc.err, got, got.Error, tc.code, tc.upstream)
		}
	}
}
