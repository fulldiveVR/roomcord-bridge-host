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

// Package fakenet is a deterministic bridgev2 network connector for tests and for
// the fixtures of the client test suite. Login: any phone number, then code 111111.
package fakenet

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	up "go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
)

const (
	NetworkID = "fake"
	MagicCode = "111111"
	// ConfirmEmoji is shown by flow `emoji`. The login completes EmojiDelay after the step is shown.
	ConfirmEmoji = "🐱"
	EmojiDelay   = 30 * time.Second
	StepEmoji    = "fake.login.emoji"

	StepPhone         = "fake.login.phone"
	StepCode          = "fake.login.code"
	StepCodeIncorrect = "fake.login.code.incorrect"
	StepComplete      = "fake.login.complete"
)

type SentMessage struct {
	LoginID networkid.UserLoginID
	ChatID  networkid.PortalID
	ID      networkid.MessageID
	Text    string
	ReplyTo networkid.MessageID
}

type Connector struct {
	Bridge *bridgev2.Bridge

	id string

	mu        sync.Mutex
	clients   map[networkid.UserLoginID]*Client
	sent      []SentMessage
	loggedOut []networkid.UserLoginID
	counter   atomic.Int64
	// revoked logins buffer remote events in pending until the next Connect, like a
	// network that delivers what arrived while the session was dead after a re-login.
	revoked map[networkid.UserLoginID]bool
	pending map[networkid.UserLoginID][]bridgev2.RemoteEvent
	// seedMedia is the attachment id of the seeded media message per login.
	seedMedia map[networkid.UserLoginID]string
}

var (
	_ bridgev2.NetworkConnector     = (*Connector)(nil)
	_ bridgev2.NetworkAPI           = (*Client)(nil)
	_ bridgev2.NetworkAPIWithUserID = (*Client)(nil)
)

// New runs the fake network under NetworkID.
func New() *Connector { return NewWithID(NetworkID) }

// NewWithID runs the fake network under another network id (BRIDGE_HOST_FAKE_ID).
func NewWithID(id string) *Connector {
	return &Connector{
		id:        id,
		clients:   make(map[networkid.UserLoginID]*Client),
		revoked:   make(map[networkid.UserLoginID]bool),
		pending:   make(map[networkid.UserLoginID][]bridgev2.RemoteEvent),
		seedMedia: make(map[networkid.UserLoginID]string),
	}
}

func (fc *Connector) Init(br *bridgev2.Bridge)        { fc.Bridge = br }
func (fc *Connector) Start(ctx context.Context) error { return nil }

// ID is the network id the host serves this fake under.
func (fc *Connector) ID() string { return fc.id }

func (fc *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{DisplayName: "Fake", NetworkID: fc.id, BeeperBridgeType: fc.id, DefaultCommandPrefix: "!fake"}
}

func (fc *Connector) GetDBMetaTypes() database.MetaTypes { return database.MetaTypes{} }

func (fc *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

func (fc *Connector) GetConfig() (string, any, up.Upgrader) { return "", nil, nil }

func (fc *Connector) GetBridgeInfoVersion() (int, int) { return 1, 1 }

func (fc *Connector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	c := &Client{main: fc, login: login}
	login.Client = c
	fc.mu.Lock()
	fc.clients[login.ID] = c
	fc.mu.Unlock()
	return nil
}

func (fc *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{ID: "phone", Name: "Phone", Description: "Any phone number, then code 111111"},
		{ID: "emoji", Name: "Emoji", Description: "Confirm an emoji in the other app; completes after 30 s"},
	}
}

func (fc *Connector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case "phone":
		return &login{main: fc, user: user}, nil
	case "emoji":
		return &emojiLogin{main: fc, user: user, n: fc.counter.Add(1)}, nil
	}
	return nil, bridgev2.ErrInvalidLoginFlowID
}

// Client returns the client of a login, or nil.
func (fc *Connector) Client(loginID networkid.UserLoginID) *Client {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.clients[loginID]
}

// Sent returns every message the network accepted from the host.
func (fc *Connector) Sent() []SentMessage {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]SentMessage(nil), fc.sent...)
}

func (fc *Connector) LoggedOut() []networkid.UserLoginID {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]networkid.UserLoginID(nil), fc.loggedOut...)
}

type login struct {
	main  *Connector
	user  *bridgev2.User
	phone string
}

var phoneStep = &bridgev2.LoginStep{
	Type:   bridgev2.LoginStepTypeUserInput,
	StepID: StepPhone,
	UserInputParams: &bridgev2.LoginUserInputParams{Fields: []bridgev2.LoginInputDataField{{
		Type: bridgev2.LoginInputFieldTypePhoneNumber, ID: "phone_number", Name: "Phone number",
	}}},
}

func codeStep(id, instructions string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       id,
		Instructions: instructions,
		UserInputParams: &bridgev2.LoginUserInputParams{Fields: []bridgev2.LoginInputDataField{{
			Type: bridgev2.LoginInputFieldType2FACode, ID: "code", Name: "Code",
		}}},
	}
}

func (l *login) Start(ctx context.Context) (*bridgev2.LoginStep, error) { return phoneStep, nil }
func (l *login) Cancel()                                                {}

func (l *login) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if l.phone == "" {
		l.phone = input["phone_number"]
		return codeStep(StepCode, "Enter the code (always 111111)"), nil
	}
	if input["code"] != MagicCode {
		return codeStep(StepCodeIncorrect, "Incorrect code"), nil
	}
	loginID := networkid.UserLoginID(strings.TrimPrefix(l.phone, "+"))
	ul, err := l.user.NewLogin(ctx, &database.UserLogin{
		ID:            loginID,
		RemoteName:    l.phone,
		RemoteProfile: status.RemoteProfile{Phone: l.phone, Name: "Fake " + l.phone},
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, err
	}
	ul.Client.Connect(ul.Log.WithContext(l.main.Bridge.BackgroundCtx))
	return &bridgev2.LoginStep{
		Type:           bridgev2.LoginStepTypeComplete,
		StepID:         StepComplete,
		Instructions:   "Logged in as " + l.phone,
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}

type emojiLogin struct {
	main *Connector
	user *bridgev2.User
	n    int64
}

var _ bridgev2.LoginProcessDisplayAndWait = (*emojiLogin)(nil)

func (l *emojiLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:                 bridgev2.LoginStepTypeDisplayAndWait,
		StepID:               StepEmoji,
		Instructions:         "Tap the same emoji in the Fake app.",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeEmoji, Data: ConfirmEmoji},
	}, nil
}

func (l *emojiLogin) Cancel() {}

func (l *emojiLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(EmojiDelay):
	}
	name := fmt.Sprintf("emoji-%d", l.n)
	ul, err := l.user.NewLogin(ctx, &database.UserLogin{
		ID: networkid.UserLoginID(name), RemoteName: name, RemoteProfile: status.RemoteProfile{Name: "Fake " + name},
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, err
	}
	ul.Client.Connect(ul.Log.WithContext(l.main.Bridge.BackgroundCtx))
	return &bridgev2.LoginStep{
		Type: bridgev2.LoginStepTypeComplete, StepID: StepComplete, Instructions: "Logged in as " + name,
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}

type Client struct {
	main      *Connector
	login     *bridgev2.UserLogin
	connected atomic.Bool
}

func (c *Client) SelfID() networkid.UserID    { return networkid.UserID("u-" + string(c.login.ID)) }
func (c *Client) GetUserID() networkid.UserID { return c.SelfID() }
func (c *Client) Login() *bridgev2.UserLogin  { return c.login }
func (c *Client) IsLoggedIn() bool            { return c.connected.Load() }
func (c *Client) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return userID == c.SelfID()
}

func (c *Client) Connect(ctx context.Context) {
	c.connected.Store(true)
	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	c.main.mu.Lock()
	pending := c.main.pending[c.login.ID]
	delete(c.main.pending, c.login.ID)
	delete(c.main.revoked, c.login.ID)
	c.main.mu.Unlock()
	for _, evt := range pending {
		c.login.QueueRemoteEvent(evt)
	}
}

// Revoke ends the session from the network side: BAD_CREDENTIALS, and remote events
// are held back until the next login of the same account connects.
func (c *Client) Revoke() {
	c.connected.Store(false)
	c.main.mu.Lock()
	c.main.revoked[c.login.ID] = true
	c.main.mu.Unlock()
	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Error: "FAKE_REVOKED"})
}

func (c *Client) queue(evt bridgev2.RemoteEvent) bridgev2.EventHandlingResult {
	c.main.mu.Lock()
	if c.main.revoked[c.login.ID] {
		c.main.pending[c.login.ID] = append(c.main.pending[c.login.ID], evt)
		c.main.mu.Unlock()
		return bridgev2.EventHandlingResultQueued
	}
	c.main.mu.Unlock()
	return c.login.QueueRemoteEvent(evt)
}

func (c *Client) Disconnect() { c.connected.Store(false) }

func (c *Client) LogoutRemote(ctx context.Context) {
	c.connected.Store(false)
	c.main.mu.Lock()
	c.main.loggedOut = append(c.main.loggedOut, c.login.ID)
	delete(c.main.clients, c.login.ID)
	c.main.mu.Unlock()
}

// Chat ids decide the chat shape: "dm-<user>" is a DM with <user>, anything else a group.
func (c *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	chatID := string(portal.ID)
	members := []bridgev2.ChatMember{{EventSender: bridgev2.EventSender{IsFromMe: true, Sender: c.SelfID()}, Membership: event.MembershipJoin}}
	info := &bridgev2.ChatInfo{Members: &bridgev2.ChatMemberList{IsFull: true}}
	if other, ok := strings.CutPrefix(chatID, "dm-"); ok {
		roomType := database.RoomTypeDM
		info.Type = &roomType
		info.Members.OtherUserID = networkid.UserID(other)
		members = append(members, bridgev2.ChatMember{EventSender: bridgev2.EventSender{Sender: networkid.UserID(other)}, Membership: event.MembershipJoin})
	} else {
		name := "Group " + chatID
		info.Name = &name
		members = append(members,
			bridgev2.ChatMember{EventSender: bridgev2.EventSender{Sender: "alice"}, Membership: event.MembershipJoin},
			bridgev2.ChatMember{EventSender: bridgev2.EventSender{Sender: "bob"}, Membership: event.MembershipJoin},
		)
	}
	info.Members.Members = members
	return info, nil
}

func (c *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	name := "User " + string(ghost.ID)
	if ghost.ID == c.SelfID() {
		name = c.login.RemoteName
	}
	return &bridgev2.UserInfo{Name: &name}, nil
}

func (c *Client) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	anyFile := &event.FileFeatures{MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported}, Caption: event.CapLevelFullySupported}
	return &event.RoomFeatures{
		ID: "fake", Reply: event.CapLevelFullySupported, Edit: event.CapLevelFullySupported, Reaction: event.CapLevelFullySupported,
		File: event.FileFeatureMap{event.MsgImage: anyFile, event.MsgVideo: anyFile, event.MsgAudio: anyFile, event.MsgFile: anyFile},
	}
}

func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	id := networkid.MessageID(fmt.Sprintf("out-%d", c.main.counter.Add(1)))
	sent := SentMessage{LoginID: c.login.ID, ChatID: msg.Portal.ID, ID: id, Text: msg.Content.Body}
	if msg.ReplyTo != nil {
		sent.ReplyTo = msg.ReplyTo.ID
	}
	c.main.mu.Lock()
	c.main.sent = append(c.main.sent, sent)
	c.main.mu.Unlock()
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{ID: id, SenderID: c.SelfID(), Timestamp: time.Now()}}, nil
}

func (c *Client) meta(chatID, senderID string, t bridgev2.RemoteEventType) simplevent.EventMeta {
	return c.metaAt(chatID, senderID, t, time.Now())
}

func (c *Client) metaAt(chatID, senderID string, t bridgev2.RemoteEventType, ts time.Time) simplevent.EventMeta {
	sender := bridgev2.EventSender{Sender: networkid.UserID(senderID)}
	if networkid.UserID(senderID) == c.SelfID() {
		sender.IsFromMe = true
	}
	return simplevent.EventMeta{
		Type:         t,
		PortalKey:    networkid.PortalKey{ID: networkid.PortalID(chatID), Receiver: c.login.ID},
		Sender:       sender,
		CreatePortal: true,
		Timestamp:    ts,
	}
}

func textConverter(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, text string) (*bridgev2.ConvertedMessage, error) {
	return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
		Type:    event.EventMessage,
		Content: &event.MessageEventContent{MsgType: event.MsgText, Body: text},
	}}}, nil
}

// Incoming queues a remote text message from senderID into chatID.
func (c *Client) Incoming(chatID, senderID, msgID, text string) bridgev2.EventHandlingResult {
	return c.IncomingAt(chatID, senderID, msgID, text, nil, time.Now())
}

// File is an attachment of a remote message.
type File struct {
	FileName string
	MimeType string
	Data     []byte
}

// IncomingAt queues a remote message with an optional attachment (text is the caption).
// onUpload receives the attachment id that the host assigned to the file.
func (c *Client) IncomingAt(chatID, senderID, msgID, text string, file *File, ts time.Time, onUpload ...func(attachmentID string)) bridgev2.EventHandlingResult {
	convert := textConverter
	if file != nil {
		convert = func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, text string) (*bridgev2.ConvertedMessage, error) {
			uri, encrypted, err := intent.UploadMedia(ctx, portal.MXID, file.Data, file.FileName, file.MimeType)
			if err != nil {
				return nil, err
			}
			if parsed, err := uri.Parse(); err == nil {
				for _, fn := range onUpload {
					fn(parsed.FileID)
				}
			}
			body := text
			if body == "" {
				body = file.FileName
			}
			content := &event.MessageEventContent{
				MsgType: msgTypeFor(file.MimeType), Body: body, FileName: file.FileName, URL: uri, File: encrypted,
				Info: &event.FileInfo{MimeType: file.MimeType, Size: len(file.Data)},
			}
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: content}}}, nil
		}
	}
	return c.queue(&simplevent.Message[string]{
		EventMeta:          c.metaAt(chatID, senderID, bridgev2.RemoteEventMessage, ts),
		ID:                 networkid.MessageID(msgID),
		Data:               text,
		ConvertMessageFunc: convert,
	})
}

func msgTypeFor(mime string) event.MessageType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mime, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mime, "audio/"):
		return event.MsgAudio
	default:
		return event.MsgFile
	}
}

func (c *Client) Edit(chatID, senderID, msgID, text string) bridgev2.EventHandlingResult {
	meta := c.meta(chatID, senderID, bridgev2.RemoteEventEdit)
	meta.CreatePortal = false
	return c.queue(&simplevent.Message[string]{
		EventMeta:     meta,
		ID:            networkid.MessageID(msgID),
		TargetMessage: networkid.MessageID(msgID),
		Data:          text,
		ConvertEditFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, text string) (*bridgev2.ConvertedEdit, error) {
			part := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: event.MsgText, Body: text}}
			return &bridgev2.ConvertedEdit{ModifiedParts: []*bridgev2.ConvertedEditPart{part.ToEditPart(existing[0])}}, nil
		},
	})
}

func (c *Client) Delete(chatID, senderID, msgID string) bridgev2.EventHandlingResult {
	meta := c.meta(chatID, senderID, bridgev2.RemoteEventMessageRemove)
	meta.CreatePortal = false
	return c.queue(&simplevent.MessageRemove{EventMeta: meta, TargetMessage: networkid.MessageID(msgID)})
}

func (c *Client) React(chatID, senderID, msgID, emoji string, remove bool) bridgev2.EventHandlingResult {
	t := bridgev2.RemoteEventReaction
	if remove {
		t = bridgev2.RemoteEventReactionRemove
	}
	meta := c.meta(chatID, senderID, t)
	meta.CreatePortal = false
	return c.queue(&simplevent.Reaction{
		EventMeta:     meta,
		TargetMessage: networkid.MessageID(msgID),
		EmojiID:       networkid.EmojiID(emoji),
		Emoji:         emoji,
	})
}

func (c *Client) Read(chatID, senderID, msgID string) bridgev2.EventHandlingResult {
	meta := c.meta(chatID, senderID, bridgev2.RemoteEventReadReceipt)
	meta.CreatePortal = false
	return c.queue(&simplevent.Receipt{EventMeta: meta, LastTarget: networkid.MessageID(msgID)})
}

// SetState pushes a bridge state for the login, e.g. BAD_CREDENTIALS.
func (c *Client) SetState(evt status.BridgeStateEvent) {
	c.login.BridgeState.Send(status.BridgeState{StateEvent: evt})
}
