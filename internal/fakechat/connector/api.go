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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// APIError is an error answer of the FakeChat server.
type APIError struct {
	Status       int
	Code         string
	Message      string
	AttemptsLeft *int
	RetryAfter   int
}

func (e *APIError) set(b proto.Error) {
	e.Code, e.Message, e.AttemptsLeft, e.RetryAfter = b.Code, b.Message, b.AttemptsLeft, b.RetryAfter
}

func (e *APIError) Error() string {
	return fmt.Sprintf("fakechat: %d %s: %s", e.Status, e.Code, e.Message)
}

func IsUnauthorized(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusUnauthorized && ae.Code == proto.ErrUnauthorized
}

// API is an HTTP client of the FakeChat network API.
type API struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func NewAPI(base, token string) *API {
	return &API{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func (a *API) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.Base+path, body)
	if err != nil {
		return nil, err
	}
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	return req, nil
}

func (a *API) send(req *http.Request, out any) error {
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var body proto.ErrorBody
		ae := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(raw, &body) == nil && body.Error.Code != "" {
			ae.set(body.Error)
		} else {
			ae.Code, ae.Message = proto.ErrServerError, http.StatusText(resp.StatusCode)
		}
		if ae.RetryAfter == 0 {
			ae.RetryAfter, _ = strconv.Atoi(resp.Header.Get("Retry-After"))
		}
		return ae
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *API) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := a.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return a.send(req, out)
}

func esc(s string) string { return url.PathEscape(s) }

func (a *API) QRStart(ctx context.Context) (out proto.QRStart, err error) {
	err = a.do(ctx, "POST", "/api/v1/auth/qr", struct{}{}, &out)
	return
}

func (a *API) QRPoll(ctx context.Context, qrID string, wait time.Duration) (out proto.QRStatus, err error) {
	err = a.do(ctx, "GET", fmt.Sprintf("/api/v1/auth/qr/%s?wait=%d", esc(qrID), int(wait.Seconds())), nil, &out)
	return
}

func (a *API) PhoneStart(ctx context.Context, phone string) (out proto.Challenge, err error) {
	err = a.do(ctx, "POST", "/api/v1/auth/phone", proto.PhoneStart{Phone: phone}, &out)
	return
}

func (a *API) PhoneVerify(ctx context.Context, challengeID, code string) (out proto.Session, err error) {
	err = a.do(ctx, "POST", "/api/v1/auth/phone/verify", proto.CodeSubmit{ChallengeID: challengeID, Code: code}, &out)
	return
}

func (a *API) Password(ctx context.Context, username, password string) (out proto.PasswordResult, err error) {
	err = a.do(ctx, "POST", "/api/v1/auth/password", proto.PasswordLogin{Username: username, Password: password}, &out)
	return
}

func (a *API) TwoFactor(ctx context.Context, challengeID, code string) (out proto.Session, err error) {
	err = a.do(ctx, "POST", "/api/v1/auth/2fa", proto.CodeSubmit{ChallengeID: challengeID, Code: code}, &out)
	return
}

func (a *API) WebExchange(ctx context.Context, session string) (out proto.Session, err error) {
	err = a.do(ctx, "POST", "/api/v1/auth/web", proto.WebExchange{Session: session}, &out)
	return
}

func (a *API) Me(ctx context.Context) (out proto.Account, err error) {
	err = a.do(ctx, "GET", "/api/v1/me", nil, &out)
	return
}

func (a *API) Logout(ctx context.Context) error { return a.do(ctx, "POST", "/api/v1/logout", nil, nil) }

func (a *API) User(ctx context.Context, id string) (out proto.User, err error) {
	err = a.do(ctx, "GET", "/api/v1/users/"+esc(id), nil, &out)
	return
}

func (a *API) Chats(ctx context.Context) (out []proto.Chat, err error) {
	err = a.do(ctx, "GET", "/api/v1/chats", nil, &out)
	return
}

func (a *API) Chat(ctx context.Context, id string) (out proto.Chat, err error) {
	err = a.do(ctx, "GET", "/api/v1/chats/"+esc(id), nil, &out)
	return
}

// Messages pages a chat. before and after are message ids; at most one is set.
func (a *API) Messages(ctx context.Context, chatID, before, after string, limit int) (out proto.MessagePage, err error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != "" {
		q.Set("before", before)
	}
	if after != "" {
		q.Set("after", after)
	}
	err = a.do(ctx, "GET", "/api/v1/chats/"+esc(chatID)+"/messages?"+q.Encode(), nil, &out)
	return
}

func (a *API) Send(ctx context.Context, chatID string, msg proto.SendMessage) (out proto.Message, err error) {
	err = a.do(ctx, "POST", "/api/v1/chats/"+esc(chatID)+"/messages", msg, &out)
	return
}

func (a *API) Edit(ctx context.Context, chatID, msgID string, edit proto.EditMessage) (out proto.Message, err error) {
	err = a.do(ctx, "PATCH", "/api/v1/chats/"+esc(chatID)+"/messages/"+esc(msgID), edit, &out)
	return
}

func (a *API) Delete(ctx context.Context, chatID, msgID string) error {
	return a.do(ctx, "DELETE", "/api/v1/chats/"+esc(chatID)+"/messages/"+esc(msgID), nil, nil)
}

func (a *API) React(ctx context.Context, chatID, msgID, emoji string, add bool) error {
	method := "PUT"
	if !add {
		method = "DELETE"
	}
	return a.do(ctx, method, "/api/v1/chats/"+esc(chatID)+"/messages/"+esc(msgID)+"/reactions/"+esc(emoji), nil, nil)
}

func (a *API) Read(ctx context.Context, chatID, msgID string) error {
	return a.do(ctx, "POST", "/api/v1/chats/"+esc(chatID)+"/read", proto.ReadBody{MessageID: msgID}, nil)
}

func (a *API) Typing(ctx context.Context, chatID string, typing bool) error {
	return a.do(ctx, "POST", "/api/v1/chats/"+esc(chatID)+"/typing", proto.TypingBody{Typing: typing}, nil)
}

// UploadInfo is optional metadata of an upload.
type UploadInfo struct {
	Kind                      string
	Width, Height, DurationMs int
}

func (a *API) Upload(ctx context.Context, name, mime string, data []byte, info UploadInfo) (out proto.Media, err error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	if mime != "" {
		h.Set("Content-Type", mime)
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		return out, err
	}
	_, _ = part.Write(data)
	if err = mw.Close(); err != nil {
		return out, err
	}
	q := url.Values{}
	if info.Kind != "" {
		q.Set("kind", info.Kind)
	}
	if info.Width > 0 {
		q.Set("width", strconv.Itoa(info.Width))
	}
	if info.Height > 0 {
		q.Set("height", strconv.Itoa(info.Height))
	}
	if info.DurationMs > 0 {
		q.Set("durationMs", strconv.Itoa(info.DurationMs))
	}
	req, err := a.newRequest(ctx, "POST", "/api/v1/media?"+q.Encode(), &buf)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	err = a.send(req, &out)
	return
}

func (a *API) Download(ctx context.Context, mediaID string) ([]byte, error) {
	req, err := a.newRequest(ctx, "GET", "/api/v1/media/"+esc(mediaID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Status: resp.StatusCode, Code: proto.ErrServerError, Message: "media download failed"}
	}
	return io.ReadAll(resp.Body)
}

// DialEvents opens the event stream. since < 0 starts at the head.
func (a *API) DialEvents(ctx context.Context, since int64) (*websocket.Conn, error) {
	u := strings.Replace(a.Base, "http", "ws", 1) + "/api/v1/events"
	if since >= 0 {
		u += "?since=" + strconv.FormatInt(since, 10)
	}
	conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + a.Token}}})
	if err != nil {
		if resp != nil {
			ae := &APIError{Status: resp.StatusCode, Code: proto.ErrServerError, Message: "event stream refused"}
			var body proto.ErrorBody
			if resp.Body != nil {
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				if json.Unmarshal(raw, &body) == nil && body.Error.Code != "" {
					ae.set(body.Error)
				}
			}
			return nil, ae
		}
		return nil, err
	}
	conn.SetReadLimit(4 << 20)
	return conn, nil
}
