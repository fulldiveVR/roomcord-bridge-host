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

package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// Flow and step ids. A step id with the suffix .incorrect or .invalid re-asks the
// same input after a wrong answer (the host maps it to the base step with error).
const (
	FlowQR       = "qr"
	FlowPhone    = "phone"
	FlowPassword = "password"
	FlowWeb      = "web"

	StepQR          = "fakechat.login.qr"
	StepPhone       = "fakechat.login.phone"
	StepSMSCode     = "fakechat.login.sms_code"
	StepCredentials = "fakechat.login.credentials"
	StepTwoFactor   = "fakechat.login.2fa"
	StepWeb         = "fakechat.login.web"
	StepComplete    = "fakechat.login.complete"

	// QRRefreshes is how many new codes Wait shows before the login fails with expired.
	QRRefreshes = 3

	WebSessionCookie = "fc_session"
	WebUserCookie    = "fc_user"
	WebSessionRegex  = "^fcs_[0-9a-f]+$"
)

func (fc *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{ID: FlowQR, Name: "QR code", Description: "Scan the code with the FakeChat phone page (/phone)"},
		{ID: FlowPhone, Name: "Phone number", Description: "Phone number, then the SMS code 123456"},
		{ID: FlowPassword, Name: "Password", Description: "Username and password, then a two-factor code when the account has one"},
		{ID: FlowWeb, Name: "Browser", Description: "Sign in on the FakeChat web page; the client captures the session cookie"},
	}
}

func (fc *Connector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	base := loginBase{main: fc, user: user, api: NewAPI(fc.Config.URL, "")}
	switch flowID {
	case FlowQR:
		return &qrLogin{loginBase: base}, nil
	case FlowPhone:
		return &phoneLogin{loginBase: base}, nil
	case FlowPassword:
		return &passwordLogin{loginBase: base}, nil
	case FlowWeb:
		return &webLogin{loginBase: base}, nil
	}
	return nil, bridgev2.ErrInvalidLoginFlowID
}

type loginBase struct {
	main *Connector
	user *bridgev2.User
	api  *API
}

func (l *loginBase) Cancel() {}

// upstreamErr turns a server error into an error that the host classifies:
// 429 becomes rate_limited, the rest internal_error with the server code.
func upstreamErr(err error) error {
	var ae *APIError
	if !errors.As(err, &ae) {
		return err
	}
	re := mautrix.RespError{ErrCode: "FC_" + ae.Code, Err: ae.Message, StatusCode: ae.Status}
	if ae.Status == http.StatusTooManyRequests {
		re.ErrCode = "FC_RATE_LIMITED"
	}
	return re
}

func apiCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// finish creates or updates the login and connects it.
func (l *loginBase) finish(ctx context.Context, sess proto.Session) (*bridgev2.LoginStep, error) {
	acc := sess.Account
	name := acc.DisplayName
	ul, err := l.user.NewLogin(ctx, &database.UserLogin{
		ID:         networkid.UserLoginID(acc.ID),
		RemoteName: name,
		RemoteProfile: status.RemoteProfile{
			Name: name, Phone: acc.Phone, Username: acc.Username,
		},
		Metadata: &UserLoginMetadata{AccessToken: sess.AccessToken},
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, err
	}
	ul.Client.Connect(ul.Log.WithContext(l.main.Bridge.BackgroundCtx))
	return &bridgev2.LoginStep{
		Type:           bridgev2.LoginStepTypeComplete,
		StepID:         StepComplete,
		Instructions:   "Logged in as " + name,
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}

func inputStep(stepID, instructions string, fields ...bridgev2.LoginInputDataField) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type: bridgev2.LoginStepTypeUserInput, StepID: stepID, Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{Fields: fields},
	}
}

func codeField() bridgev2.LoginInputDataField {
	return bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldType2FACode, ID: "code", Name: "Code"}
}

// ---- QR

type qrLogin struct {
	loginBase
	qr        proto.QRStart
	refreshes int
}

var _ bridgev2.LoginProcessDisplayAndWait = (*qrLogin)(nil)

// Cancel withdraws the shown code, like closing "Link a device" on the computer.
func (l *qrLogin) Cancel() {
	if l.qr.QRID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = l.api.QRCancel(ctx, l.qr.QRID)
}

func (l *qrLogin) step() *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:                 bridgev2.LoginStepTypeDisplayAndWait,
		StepID:               StepQR,
		Instructions:         "Open " + l.qr.PhoneURL + " and approve the code " + l.qr.QRID + ".",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeQR, Data: l.qr.Data},
	}
}

func (l *qrLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	qr, err := l.api.QRStart(ctx)
	if err != nil {
		return nil, upstreamErr(err)
	}
	l.qr = qr
	return l.step(), nil
}

// Wait long-polls the server. An expired code is replaced by a new one (same step
// id, new data) up to QRRefreshes times, like WhatsApp; then the login fails.
func (l *qrLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	for {
		st, err := l.api.QRPoll(ctx, l.qr.QRID, 20*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ae *APIError
			if errors.As(err, &ae) && ae.Status >= 500 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return nil, upstreamErr(err)
		}
		switch st.Status {
		case proto.QRApproved:
			if st.Session == nil {
				return nil, errors.New("the server approved the code without a session")
			}
			return l.finish(ctx, *st.Session)
		case proto.QRExpired:
			if l.refreshes >= QRRefreshes {
				return nil, mautrix.RespError{ErrCode: "FC_QR_TIMEOUT", Err: "the QR code was not scanned in time"}
			}
			l.refreshes++
			qr, err := l.api.QRStart(ctx)
			if err != nil {
				return nil, upstreamErr(err)
			}
			l.qr = qr
			return l.step(), nil
		}
	}
}

// ---- phone and SMS code

type phoneLogin struct {
	loginBase
	challenge string
}

var _ bridgev2.LoginProcessUserInput = (*phoneLogin)(nil)

func phoneStep(id, instructions string) *bridgev2.LoginStep {
	return inputStep(id, instructions, bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypePhoneNumber, ID: "phone", Name: "Phone number"})
}

func (l *phoneLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return phoneStep(StepPhone, "Enter your phone number with the country code."), nil
}

func (l *phoneLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if l.challenge == "" {
		ch, err := l.api.PhoneStart(ctx, input["phone"])
		if apiCode(err) == proto.ErrInvalidPhone {
			return phoneStep(StepPhone+".invalid", "FakeChat does not accept this phone number."), nil
		} else if err != nil {
			return nil, upstreamErr(err)
		}
		l.challenge = ch.ChallengeID
		return inputStep(StepSMSCode, "Enter the code from the SMS (always 123456).", codeField()), nil
	}
	sess, err := l.api.PhoneVerify(ctx, l.challenge, input["code"])
	if apiCode(err) == proto.ErrInvalidCode {
		var ae *APIError
		errors.As(err, &ae)
		msg := "The code is wrong."
		if ae.AttemptsLeft != nil {
			msg = fmt.Sprintf("The code is wrong. %d attempts left.", *ae.AttemptsLeft)
		}
		return inputStep(StepSMSCode+".incorrect", msg, codeField()), nil
	} else if err != nil {
		return nil, upstreamErr(err)
	}
	return l.finish(ctx, sess)
}

// ---- username, password and optional 2FA

type passwordLogin struct {
	loginBase
	challenge string
}

var _ bridgev2.LoginProcessUserInput = (*passwordLogin)(nil)

func credentialsStep(id, instructions string) *bridgev2.LoginStep {
	return inputStep(id, instructions,
		bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypeUsername, ID: "username", Name: "Username"},
		bridgev2.LoginInputDataField{Type: bridgev2.LoginInputFieldTypePassword, ID: "password", Name: "Password"},
	)
}

func (l *passwordLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return credentialsStep(StepCredentials, "Sign in with your FakeChat username and password."), nil
}

func (l *passwordLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if l.challenge == "" {
		res, err := l.api.Password(ctx, input["username"], input["password"])
		if apiCode(err) == proto.ErrInvalidPassword {
			return credentialsStep(StepCredentials+".incorrect", "Wrong username or password."), nil
		} else if err != nil {
			return nil, upstreamErr(err)
		}
		if res.TwoFactorRequired {
			l.challenge = res.ChallengeID
			return inputStep(StepTwoFactor, "Enter the code from your authenticator app.", codeField()), nil
		}
		return l.finish(ctx, res.Session)
	}
	sess, err := l.api.TwoFactor(ctx, l.challenge, input["code"])
	if apiCode(err) == proto.ErrInvalidCode {
		return inputStep(StepTwoFactor+".incorrect", "The two-factor code is wrong.", codeField()), nil
	} else if err != nil {
		return nil, upstreamErr(err)
	}
	return l.finish(ctx, sess)
}

// ---- browser session capture

type webLogin struct {
	loginBase
}

var _ bridgev2.LoginProcessCookies = (*webLogin)(nil)

// Start asks the client to open the FakeChat web login and to read the httpOnly
// session cookie after the redirect to /web/app.
func (l *webLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	base := l.main.webURL()
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errors.New("the FakeChat web URL is not valid")
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeCookies,
		StepID:       StepWeb,
		Instructions: "Sign in to FakeChat in the browser window.",
		CookiesParams: &bridgev2.LoginCookiesParams{
			URL: base + "/web/login",
			Fields: []bridgev2.LoginCookieField{
				{ID: "session", Required: true, Pattern: WebSessionRegex, Sources: []bridgev2.LoginCookieFieldSource{
					{Type: bridgev2.LoginCookieTypeCookie, Name: WebSessionCookie, CookieDomain: u.Hostname()},
				}},
				{ID: "user", Required: false, Sources: []bridgev2.LoginCookieFieldSource{
					{Type: bridgev2.LoginCookieTypeCookie, Name: WebUserCookie, CookieDomain: u.Hostname()},
				}},
			},
			WaitForURLPattern: "^" + regexp.QuoteMeta(u.Scheme+"://"+u.Host) + "/web/app(?:[?#].*)?$",
		},
	}, nil
}

func (l *webLogin) SubmitCookies(ctx context.Context, cookies map[string]string) (*bridgev2.LoginStep, error) {
	sess, err := l.api.WebExchange(ctx, cookies["session"])
	if err != nil {
		return nil, upstreamErr(err)
	}
	return l.finish(ctx, sess)
}
