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

// Package wire holds the JSON shapes of the bridge-host protocol (see PROTOCOL.md).
package wire

import "time"

const (
	EventCheckpoint         = "checkpoint"
	EventConversationUpsert = "conversation_upsert"
	EventParticipantUpsert  = "participant_upsert"
	EventMessageUpsert      = "message_upsert"
	EventMessageDelete      = "message_delete"
	EventReactionUpsert     = "reaction_upsert"
	EventReactionDelete     = "reaction_delete"
	EventReadReceipt        = "read_receipt"
	EventAccountStatus      = "account_status"
)

type Frame struct {
	Seq     int64  `json:"seq"`
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type Attachment struct {
	Network              string  `json:"network"`
	ExternalAttachmentID string  `json:"externalAttachmentId"`
	Kind                 string  `json:"kind"`
	MimeType             string  `json:"mimeType"`
	FileName             *string `json:"fileName"`
	SizeBytes            *int64  `json:"sizeBytes"`
	Width                *int    `json:"width"`
	Height               *int    `json:"height"`
	DurationMs           *int    `json:"durationMs"`
}

type Reaction struct {
	Network          string     `json:"network"`
	SenderExternalID string     `json:"senderExternalId"`
	Emoji            string     `json:"emoji"`
	ReactedAt        *time.Time `json:"reactedAt"`
}

type Message struct {
	Network                  string       `json:"network"`
	ExternalConversationID   string       `json:"externalConversationId"`
	ExternalMessageID        string       `json:"externalMessageId"`
	Direction                string       `json:"direction"`
	SenderExternalID         string       `json:"senderExternalId"`
	SenderName               *string      `json:"senderName"`
	Text                     string       `json:"text"`
	Subject                  *string      `json:"subject"`
	ReplyToExternalMessageID *string      `json:"replyToExternalMessageId"`
	Attachments              []Attachment `json:"attachments"`
	Reactions                []Reaction   `json:"reactions"`
	Status                   string       `json:"status"`
	SentAt                   time.Time    `json:"sentAt"`
	EditedAt                 *time.Time   `json:"editedAt"`
	DeletedAt                *time.Time   `json:"deletedAt"`
}

type Participant struct {
	Network               string  `json:"network"`
	ExternalParticipantID string  `json:"externalParticipantId"`
	DisplayName           string  `json:"displayName"`
	Handle                *string `json:"handle"`
	AvatarURL             *string `json:"avatarUrl"`
	IsSelf                bool    `json:"isSelf"`
}

type Conversation struct {
	Network                   string   `json:"network"`
	SubNetwork                *string  `json:"subNetwork"`
	ExternalConversationID    string   `json:"externalConversationId"`
	Kind                      string   `json:"kind"`
	Title                     string   `json:"title"`
	AvatarURL                 *string  `json:"avatarUrl"`
	ParticipantExternalIDs    []string `json:"participantExternalIds"`
	UnreadCount               *int     `json:"unreadCount"`
	LastReadExternalMessageID *string  `json:"lastReadExternalMessageId"`
}

type MessageUpsertPayload struct {
	Message      Message       `json:"message"`
	Participants []Participant `json:"participants,omitempty"`
}

type MessageDeletePayload struct {
	ExternalConversationID string    `json:"externalConversationId"`
	ExternalMessageID      string    `json:"externalMessageId"`
	DeletedAt              time.Time `json:"deletedAt"`
}

type ReactionPayload struct {
	ExternalConversationID string   `json:"externalConversationId"`
	ExternalMessageID      string   `json:"externalMessageId"`
	Reaction               Reaction `json:"reaction"`
	// Reactions is the full list of the message after this change. The store fills it.
	Reactions []Reaction `json:"reactions"`
}

type ReadReceiptPayload struct {
	ExternalConversationID    string `json:"externalConversationId"`
	LastReadExternalMessageID string `json:"lastReadExternalMessageId"`
	ReaderExternalID          string `json:"readerExternalId"`
	IsSelf                    bool   `json:"isSelf"`
	// UnreadCount is set for receipts of the account itself: inbound bridged messages after the read one.
	UnreadCount *int `json:"unreadCount"`
}

type ConversationUpsertPayload struct {
	Conversation Conversation  `json:"conversation"`
	Participants []Participant `json:"participants,omitempty"`
}

type ParticipantUpsertPayload struct {
	ExternalConversationID string      `json:"externalConversationId"`
	Participant            Participant `json:"participant"`
}

type AccountStatusPayload struct {
	Status string  `json:"status"`
	Code   *string `json:"code"`
	Detail string  `json:"detail,omitempty"`
}

type Account struct {
	LoginID    string `json:"loginId"`
	Network    string `json:"network"`
	UserID     string `json:"userId"`
	RemoteID   string `json:"remoteId"`
	RemoteName string `json:"remoteName"`
	State      string `json:"state"`
}

type Health struct {
	State  string    `json:"state"`
	Since  time.Time `json:"since"`
	Detail string    `json:"detail,omitempty"`
}

type Flow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Network struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	// Available is false when the host cannot log in to the network (missing config, not built in).
	Available    bool           `json:"available"`
	Reason       string         `json:"reason,omitempty"`
	Flows        []Flow         `json:"flows"`
	Capabilities map[string]any `json:"capabilities"`
}

type StepError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	AttemptsLeft *int   `json:"attemptsLeft,omitempty"`
	RetryAfter   *int   `json:"retryAfter,omitempty"`
	UpstreamCode string `json:"upstreamCode,omitempty"`
}

type FormField struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Pattern        string   `json:"pattern,omitempty"`
	Options        []string `json:"options,omitempty"`
	DefaultValue   string   `json:"defaultValue,omitempty"`
	Placeholder    string   `json:"placeholder,omitempty"`
	AutofillDomain string   `json:"autofillDomain,omitempty"`
	Optional       bool     `json:"optional,omitempty"`
	MinLength      int      `json:"minLength,omitempty"`
	MaxLength      int      `json:"maxLength,omitempty"`
}

// FormAttachment is a file shown with a form, for example a captcha image or audio clip.
type FormAttachment struct {
	Type       string `json:"type"`
	FileName   string `json:"fileName,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DataBase64 string `json:"dataBase64"`
}

// InitialCookie is a cookie the client sets before it opens the capture URL.
type InitialCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
}

type CookieSource struct {
	Type            string `json:"type"`
	Name            string `json:"name"`
	CookieDomain    string `json:"cookieDomain,omitempty"`
	RequestURLRegex string `json:"requestUrlRegex,omitempty"`
}

type CookieField struct {
	ID       string         `json:"id"`
	Required bool           `json:"required"`
	Sources  []CookieSource `json:"sources"`
	Pattern  string         `json:"pattern,omitempty"`
}

const (
	KindQR                   = "qr"
	KindPairingCode          = "pairing_code"
	KindExternalApprovalWait = "external_approval_wait"
	KindPhoneNumberInput     = "phone_number_input"
	KindCodeInput            = "code_input"
	KindPasswordInput        = "password_input"
	KindFormInput            = "form_input"
	KindWebSessionCapture    = "web_session_capture"
	KindDone                 = "done"
	KindFailed               = "failed"
)

type Step struct {
	StepID       string     `json:"stepId"`
	Kind         string     `json:"kind"`
	Instructions string     `json:"instructions,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	Pollable     bool       `json:"pollable"`
	Error        *StepError `json:"error,omitempty"`

	Data     string `json:"data,omitempty"`
	Code     string `json:"code,omitempty"`
	Emoji    string `json:"emoji,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
	FieldID  string `json:"fieldId,omitempty"`
	Fields   any    `json:"fields,omitempty"`

	Attachments []FormAttachment `json:"attachments,omitempty"`

	URL               string          `json:"url,omitempty"`
	UserAgent         string          `json:"userAgent,omitempty"`
	ExtractJS         string          `json:"extractJs,omitempty"`
	WaitForURLPattern string          `json:"waitForUrlPattern,omitempty"`
	InitialCookies    []InitialCookie `json:"initialCookies,omitempty"`
	// Hidden: the client runs the capture without showing the page (a provider script token, for example).
	Hidden bool `json:"hidden,omitempty"`
	// JSRequired: the capture cannot finish without JavaScript on the provider page. Upstream said so,
	// or a required field has only "special" sources.
	JSRequired bool `json:"jsRequired,omitempty"`

	LoginID    string `json:"loginId,omitempty"`
	RemoteID   string `json:"remoteId,omitempty"`
	RemoteName string `json:"remoteName,omitempty"`
}

type LoginResponse struct {
	ProcessID string `json:"processId"`
	Step      Step   `json:"step"`
}

type SendResponse struct {
	ExternalMessageID string       `json:"externalMessageId"`
	SentAt            time.Time    `json:"sentAt"`
	Attachments       []Attachment `json:"attachments,omitempty"`
}

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}
