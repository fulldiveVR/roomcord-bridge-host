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

// Package proto holds the JSON shapes of the FakeChat network API. The FakeChat
// server (cmd/fakechat) and the bridge connector (internal/fakechat/connector)
// both use them. README.md, section FakeChat, describes the API.
package proto

import "time"

// Message kinds.
const (
	KindText     = "text"
	KindImage    = "image"
	KindVideo    = "video"
	KindVoice    = "voice"
	KindAudio    = "audio"
	KindFile     = "file"
	KindSticker  = "sticker"
	KindLocation = "location"
	KindContact  = "contact"
	KindSystem   = "system"
)

// System message actions.
const (
	SystemMemberJoined = "member_joined"
	SystemMemberLeft   = "member_left"
	SystemRenamed      = "renamed"
)

// Event stream frame types.
const (
	EventHello          = "hello"
	EventMessage        = "message"
	EventMessageEdit    = "message_edit"
	EventMessageDelete  = "message_delete"
	EventReactionAdd    = "reaction_add"
	EventReactionRemove = "reaction_remove"
	EventRead           = "read"
	EventTyping         = "typing"
	EventChatUpdate     = "chat_update"
	EventLoggedOut      = "logged_out"
)

// CloseLoggedOut is the WebSocket close code after the server ended the session.
const CloseLoggedOut = 4001

// Error codes of the API. The HTTP status is in the docs.
const (
	ErrUnauthorized     = "unauthorized"
	ErrInvalidInput     = "invalid_input"
	ErrInvalidPhone     = "invalid_phone_number"
	ErrInvalidCode      = "invalid_code"
	ErrInvalidPassword  = "invalid_password"
	ErrInvalidSession   = "invalid_session"
	ErrExpired          = "expired"
	ErrNotFound         = "not_found"
	ErrRateLimited      = "rate_limited"
	ErrServerError      = "server_error"
	ErrMediaTooLarge    = "media_too_large"
	ErrChallengeExpired = "challenge_expired"
)

type Error struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	AttemptsLeft *int   `json:"attemptsLeft,omitempty"`
	RetryAfter   int    `json:"retryAfter,omitempty"`
}

type ErrorBody struct {
	Error Error `json:"error"`
}

type Account struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Phone       string `json:"phone,omitempty"`
	Username    string `json:"username,omitempty"`
}

// Session is the answer of every successful login.
type Session struct {
	AccessToken string  `json:"accessToken"`
	Account     Account `json:"account"`
}

type QRStart struct {
	QRID      string    `json:"qrId"`
	Data      string    `json:"data"`
	ExpiresAt time.Time `json:"expiresAt"`
	PhoneURL  string    `json:"phoneUrl"`
}

// QR statuses.
const (
	QRPending  = "pending"
	QRApproved = "approved"
	QRExpired  = "expired"
)

type QRStatus struct {
	Status  string   `json:"status"`
	Session *Session `json:"session,omitempty"`
}

type PhoneStart struct {
	Phone string `json:"phone"`
}

type Challenge struct {
	ChallengeID string `json:"challengeId"`
}

type CodeSubmit struct {
	ChallengeID string `json:"challengeId"`
	Code        string `json:"code"`
}

type PasswordLogin struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PasswordResult is a Session, or a 2FA challenge when TwoFactorRequired is set.
type PasswordResult struct {
	Session
	TwoFactorRequired bool   `json:"twoFactorRequired,omitempty"`
	ChallengeID       string `json:"challengeId,omitempty"`
}

type WebExchange struct {
	Session string `json:"session"`
}

type User struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Phone         string `json:"phone,omitempty"`
	AvatarMediaID string `json:"avatarMediaId,omitempty"`
}

type Chat struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"` // dm | group
	Title         string    `json:"title"`
	Members       []string  `json:"members"`
	AvatarMediaID string    `json:"avatarMediaId,omitempty"`
	LastMessageID string    `json:"lastMessageId,omitempty"`
	LastMessageAt time.Time `json:"lastMessageAt"`
}

type Entity struct {
	Type   string `json:"type"` // bold | italic | strike | code | pre | link
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
}

type Media struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMs int    `json:"durationMs,omitempty"`
}

type Location struct {
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Name string  `json:"name,omitempty"`
}

type Contact struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

type System struct {
	Action string `json:"action"`
	UserID string `json:"userId,omitempty"`
	Title  string `json:"title,omitempty"`
}

type Reaction struct {
	UserID string    `json:"userId"`
	Emoji  string    `json:"emoji"`
	At     time.Time `json:"at"`
}

type Message struct {
	ID        string     `json:"id"`
	ChatID    string     `json:"chatId"`
	SenderID  string     `json:"senderId"`
	TS        time.Time  `json:"ts"`
	Kind      string     `json:"kind"`
	Text      string     `json:"text,omitempty"`
	Entities  []Entity   `json:"entities,omitempty"`
	ReplyTo   string     `json:"replyTo,omitempty"`
	EditedAt  *time.Time `json:"editedAt,omitempty"`
	Deleted   bool       `json:"deleted,omitempty"`
	Reactions []Reaction `json:"reactions,omitempty"`
	Media     *Media     `json:"media,omitempty"`
	Location  *Location  `json:"location,omitempty"`
	Contact   *Contact   `json:"contact,omitempty"`
	System    *System    `json:"system,omitempty"`
}

type MessagePage struct {
	Messages []Message `json:"messages"` // oldest first
	HasMore  bool      `json:"hasMore"`
}

// SendMessage is the body of POST /api/v1/chats/{chatId}/messages.
type SendMessage struct {
	ClientID string    `json:"clientId,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Text     string    `json:"text,omitempty"`
	Entities []Entity  `json:"entities,omitempty"`
	ReplyTo  string    `json:"replyTo,omitempty"`
	MediaID  string    `json:"mediaId,omitempty"`
	Location *Location `json:"location,omitempty"`
}

type EditMessage struct {
	Text     string   `json:"text"`
	Entities []Entity `json:"entities,omitempty"`
}

type ReadBody struct {
	MessageID string `json:"messageId"`
}

type TypingBody struct {
	Typing bool `json:"typing"`
}

// Event is one frame of the event stream. Which fields are set depends on Type.
type Event struct {
	Seq       int64    `json:"seq"`
	Type      string   `json:"type"`
	ChatID    string   `json:"chatId,omitempty"`
	Message   *Message `json:"message,omitempty"`
	MessageID string   `json:"messageId,omitempty"`
	UserID    string   `json:"userId,omitempty"`
	Emoji     string   `json:"emoji,omitempty"`
	Typing    bool     `json:"typing,omitempty"`
	Chat      *Chat    `json:"chat,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	// TS is the time of the event (the edit, the reaction, the receipt).
	TS time.Time `json:"ts"`
}
