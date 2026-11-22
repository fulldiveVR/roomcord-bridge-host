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

package proto

import "time"

// The shapes below belong to the control API (/control/*), which tests and
// scripts use to drive the server. A network client never calls it.

// CreateAccount is the body of POST /control/accounts. Empty fields get defaults.
type CreateAccount struct {
	ID            string `json:"id,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	Phone         string `json:"phone,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	TwoFactorCode string `json:"twoFactorCode,omitempty"`
}

// ControlAccount is an account as the control API shows it, credentials included.
type ControlAccount struct {
	Account
	Password      string `json:"password,omitempty"`
	TwoFactorCode string `json:"twoFactorCode,omitempty"`
	Sessions      int    `json:"sessions"`
	Streams       int    `json:"streams"`
	HeadSeq       int64  `json:"headSeq"`
}

// Inject types.
const (
	InjectMessage        = "message"
	InjectEdit           = "edit"
	InjectDelete         = "delete"
	InjectReactionAdd    = "reaction_add"
	InjectReactionRemove = "reaction_remove"
	InjectRead           = "read"
	InjectTyping         = "typing"
	InjectMemberJoin     = "member_join"
	InjectMemberLeave    = "member_leave"
	InjectRename         = "rename"
)

// Inject is the body of POST /control/accounts/{id}/inject: one remote action in a chat.
type Inject struct {
	Type     string    `json:"type,omitempty"` // default message
	ChatID   string    `json:"chatId"`
	From     string    `json:"from,omitempty"` // default: the first other member
	Text     string    `json:"text,omitempty"`
	Entities []Entity  `json:"entities,omitempty"`
	ReplyTo  string    `json:"replyTo,omitempty"`
	Media    string    `json:"media,omitempty"` // catalog name, GET /control/media
	Kind     string    `json:"kind,omitempty"`  // overrides the media kind (for example voice)
	Location *Location `json:"location,omitempty"`
	Contact  *Contact  `json:"contact,omitempty"`
	// MessageID is the target of edit, delete, reaction_* and read.
	MessageID string `json:"messageId,omitempty"`
	Emoji     string `json:"emoji,omitempty"`
	Typing    *bool  `json:"typing,omitempty"`
	UserID    string `json:"userId,omitempty"` // member_join, member_leave
	Title     string `json:"title,omitempty"`  // rename
}

type InjectResult struct {
	Seq     int64    `json:"seq"`
	Message *Message `json:"message,omitempty"`
}

type ScenarioRequest struct {
	Name   string `json:"name"`
	ChatID string `json:"chatId,omitempty"`
	Count  int    `json:"count,omitempty"` // burst only
}

type ScenarioStep struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	MessageID string `json:"messageId,omitempty"`
	Seq       int64  `json:"seq"`
}

type ScenarioResult struct {
	ChatID string         `json:"chatId"`
	Steps  []ScenarioStep `json:"steps"`
}

// Sent actions.
const (
	SentSend    = "send"
	SentEdit    = "edit"
	SentDelete  = "delete"
	SentReact   = "react"
	SentUnreact = "unreact"
	SentRead    = "read"
	SentTyping  = "typing"
	SentUpload  = "upload"
)

// SentRecord is one action that a network client took (GET /control/accounts/{id}/sent).
type SentRecord struct {
	N         int       `json:"n"`
	Action    string    `json:"action"`
	ChatID    string    `json:"chatId,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
	ClientID  string    `json:"clientId,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Text      string    `json:"text,omitempty"`
	Entities  []Entity  `json:"entities,omitempty"`
	ReplyTo   string    `json:"replyTo,omitempty"`
	MediaID   string    `json:"mediaId,omitempty"`
	MediaName string    `json:"mediaName,omitempty"`
	Mime      string    `json:"mime,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Emoji     string    `json:"emoji,omitempty"`
	Typing    bool      `json:"typing,omitempty"`
	At        time.Time `json:"at"`
}

// Fault kinds and scopes for POST /control/faults.
const (
	FaultRateLimit   = "rate_limit"
	FaultServerError = "server_error"

	ScopeAny     = "any"
	ScopeAuth    = "auth"
	ScopeSend    = "send"
	ScopeHistory = "history"
	ScopeMedia   = "media"
	ScopeEvents  = "events"
)

type Fault struct {
	Kind  string `json:"kind"`
	Scope string `json:"scope,omitempty"` // default any
	Count int    `json:"count,omitempty"` // default 1
}

type CatalogEntry struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	MediaID    string `json:"mediaId"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMs int    `json:"durationMs,omitempty"`
}

type CreateChat struct {
	ID      string   `json:"id,omitempty"`
	Kind    string   `json:"kind"` // dm | group
	Title   string   `json:"title,omitempty"`
	Members []string `json:"members"` // other members; the account is added
}

type PendingQR struct {
	QRID      string    `json:"qrId"`
	Data      string    `json:"data"`
	ExpiresAt time.Time `json:"expiresAt"`
}
