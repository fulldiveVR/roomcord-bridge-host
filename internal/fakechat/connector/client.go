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
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// Bridge state error codes of this connector.
const (
	StateErrRevoked      status.BridgeStateErrorCode = "fc-session-revoked"
	StateErrDisconnected status.BridgeStateErrorCode = "fc-disconnected"
	StateErrRateLimited  status.BridgeStateErrorCode = "fc-rate-limited"
)

var (
	errLoggedOut = errors.New("the FakeChat session ended")
	// MinBackoff and MaxBackoff bound the reconnect delay.
	MinBackoff = 500 * time.Millisecond
	MaxBackoff = 10 * time.Second
	// EventWait caps the wait for bridgev2 to handle one queued remote event.
	EventWait = 15 * time.Second
)

// Client is the network client of one login.
type Client struct {
	main  *Connector
	login *bridgev2.UserLogin
	api   *API
	self  string

	loggedIn atomic.Bool

	runMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}

	seqMu   sync.Mutex
	lastSeq int64
}

var (
	_ bridgev2.NetworkAPI                    = (*Client)(nil)
	_ bridgev2.NetworkAPIWithUserID          = (*Client)(nil)
	_ bridgev2.BackfillingNetworkAPI         = (*Client)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*Client)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*Client)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*Client)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*Client)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*Client)(nil)
)

func newClient(fc *Connector, login *bridgev2.UserLogin, token string) *Client {
	c := &Client{main: fc, login: login, api: NewAPI(fc.Config.URL, token), self: string(login.ID)}
	if meta, ok := login.Metadata.(*UserLoginMetadata); ok {
		c.lastSeq = meta.LastSeq
	}
	c.loggedIn.Store(token != "")
	return c
}

func (c *Client) GetUserID() networkid.UserID { return networkid.UserID(c.self) }
func (c *Client) IsLoggedIn() bool            { return c.loggedIn.Load() }
func (c *Client) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return string(userID) == c.self
}

func (c *Client) state(evt status.BridgeStateEvent, code status.BridgeStateErrorCode, msg string) {
	c.login.BridgeState.Send(status.BridgeState{StateEvent: evt, Error: code, Message: msg})
}

// Connect starts the connection loop in the background.
func (c *Client) Connect(ctx context.Context) {
	if c.api.Token == "" {
		c.state(status.StateBadCredentials, StateErrRevoked, "no FakeChat session, log in again")
		return
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(c.login.Log.WithContext(c.main.Bridge.BackgroundCtx))
	c.cancel = cancel
	c.done = make(chan struct{})
	c.state(status.StateConnecting, "", "")
	go c.run(runCtx, c.done)
}

// Disconnect stops the loop and waits for it, so no event of this client is
// handled after it returns.
func (c *Client) Disconnect() {
	c.runMu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.runMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(EventWait + 5*time.Second):
		c.login.Log.Warn().Msg("FakeChat client loop did not stop in time")
	}
}

func (c *Client) LogoutRemote(ctx context.Context) {
	if err := c.api.Logout(ctx); err != nil && !IsUnauthorized(err) {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("FakeChat logout failed")
	}
	c.Disconnect()
	c.loggedIn.Store(false)
	c.main.dropClient(c)
}

func (c *Client) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	log := zerolog.Ctx(ctx)
	backoff := MinBackoff
	for ctx.Err() == nil {
		connected, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errLoggedOut) || IsUnauthorized(err) {
			log.Info().Msg("FakeChat ended the session")
			c.loggedIn.Store(false)
			c.state(status.StateBadCredentials, StateErrRevoked, "FakeChat ended the session, log in again")
			return
		}
		if connected {
			backoff = MinBackoff
		}
		wait := backoff
		code := StateErrDisconnected
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests {
			code = StateErrRateLimited
			wait = max(wait, time.Duration(ae.RetryAfter)*time.Second)
		}
		log.Debug().Err(err).Dur("retry_in", wait).Msg("FakeChat connection lost")
		c.state(status.StateTransientDisconnect, code, "connection to FakeChat lost, reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, MaxBackoff)
	}
}

// session runs one connection: check the token, open the stream from the last
// handled seq, resync the chats (backfill), then handle live events.
func (c *Client) session(ctx context.Context) (connected bool, err error) {
	if _, err = c.api.Me(ctx); err != nil {
		return false, err
	}
	c.seqMu.Lock()
	since := c.lastSeq
	c.seqMu.Unlock()
	conn, err := c.api.DialEvents(ctx, since)
	if err != nil {
		return false, err
	}
	defer conn.CloseNow()
	var hello proto.Event
	if err = wsjson.Read(ctx, conn, &hello); err != nil {
		return false, err
	} else if hello.Type != proto.EventHello {
		return false, fmt.Errorf("first frame is %q, not hello", hello.Type)
	}
	if hello.Reason == "cursor_ahead" {
		c.setSeq(ctx, hello.Seq)
	}
	c.state(status.StateConnected, "", "")
	if err = c.resync(ctx); err != nil {
		return true, err
	}
	for {
		var evt proto.Event
		if err = wsjson.Read(ctx, conn, &evt); err != nil {
			if websocket.CloseStatus(err) == proto.CloseLoggedOut {
				return true, errLoggedOut
			}
			return true, err
		}
		if evt.Type == proto.EventLoggedOut {
			return true, errLoggedOut
		}
		c.handleEvent(ctx, evt)
		c.setSeq(ctx, evt.Seq)
	}
}

func (c *Client) setSeq(ctx context.Context, seq int64) {
	c.seqMu.Lock()
	if seq <= c.lastSeq && seq != 0 {
		c.seqMu.Unlock()
		return
	}
	c.lastSeq = seq
	c.seqMu.Unlock()
	if meta, ok := c.login.Metadata.(*UserLoginMetadata); ok {
		meta.LastSeq = seq
		if err := c.login.Save(ctx); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to save the stream position")
		}
	}
}

// resync queues one ChatResync per chat. bridgev2 creates missing portals and runs
// a forward backfill when the chat has messages newer than the newest bridged one.
func (c *Client) resync(ctx context.Context) error {
	chats, err := c.api.Chats(ctx)
	if err != nil {
		return err
	}
	for i := len(chats) - 1; i >= 0 && ctx.Err() == nil; i-- {
		ch := chats[i]
		info := c.chatInfo(ch)
		meta, wait := c.meta(ch.ID, "", bridgev2.RemoteEventChatResync, ch.LastMessageAt)
		c.queue(ctx, &simplevent.ChatResync{
			EventMeta:       meta,
			ChatInfo:        info,
			LatestMessageTS: ch.LastMessageAt,
			CheckNeedsBackfillFunc: func(ctx context.Context, latest *database.Message) (bool, error) {
				if ch.LastMessageID == "" {
					return false, nil
				}
				return latest == nil || (string(latest.ID) != ch.LastMessageID && latest.Timestamp.Before(ch.LastMessageAt)), nil
			},
		}, wait)
	}
	return nil
}

// meta builds the event metadata. The returned channel closes when bridgev2 has
// handled the event (PostHandle).
func (c *Client) meta(chatID, senderID string, t bridgev2.RemoteEventType, ts time.Time) (simplevent.EventMeta, chan struct{}) {
	done := make(chan struct{})
	var once sync.Once
	return simplevent.EventMeta{
		Type:         t,
		PortalKey:    c.portalKey(chatID),
		Sender:       c.sender(senderID),
		CreatePortal: true,
		Timestamp:    ts,
		LogContext: func(lc zerolog.Context) zerolog.Context {
			return lc.Str("fakechat_chat", chatID)
		},
		PostHandleFunc: func(context.Context, *bridgev2.Portal) { once.Do(func() { close(done) }) },
	}, done
}

// queue hands one event to bridgev2 and waits until it is handled. Remote events
// of the whole network are handled one at a time: bridgev2 updates shared ghost
// rows without a lock while it creates a portal, so two portals that are created
// at once race (also for two logins that share a contact).
func (c *Client) queue(ctx context.Context, evt bridgev2.RemoteEvent, done chan struct{}) {
	c.main.eventMu.Lock()
	defer c.main.eventMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	res := c.login.QueueRemoteEvent(evt)
	if !res.Queued && !res.Success {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(EventWait):
		zerolog.Ctx(ctx).Warn().Str("event_type", evt.GetType().String()).Msg("Remote event was not handled in time")
	}
}

func (c *Client) portalKey(chatID string) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(chatID), Receiver: c.login.ID}
}

func (c *Client) sender(userID string) bridgev2.EventSender {
	if userID == "" {
		return bridgev2.EventSender{}
	}
	return bridgev2.EventSender{Sender: networkid.UserID(userID), IsFromMe: userID == c.self}
}

func (c *Client) handleEvent(ctx context.Context, evt proto.Event) {
	switch evt.Type {
	case proto.EventMessage:
		m := evt.Message
		if m == nil {
			return
		}
		if m.Kind == proto.KindSystem {
			c.handleSystem(ctx, evt)
			return
		}
		meta, wait := c.meta(m.ChatID, m.SenderID, bridgev2.RemoteEventMessage, m.TS)
		c.queue(ctx, &simplevent.Message[*proto.Message]{
			EventMeta: meta, ID: networkid.MessageID(m.ID), Data: m, ConvertMessageFunc: c.convertMessage,
		}, wait)
	case proto.EventMessageEdit:
		m := evt.Message
		if m == nil {
			return
		}
		meta, wait := c.meta(m.ChatID, m.SenderID, bridgev2.RemoteEventEdit, evt.TS)
		c.queue(ctx, &simplevent.Message[*proto.Message]{
			EventMeta: meta, ID: networkid.MessageID(m.ID), TargetMessage: networkid.MessageID(m.ID), Data: m, ConvertEditFunc: c.convertEdit,
		}, wait)
	case proto.EventMessageDelete:
		meta, wait := c.meta(evt.ChatID, evt.UserID, bridgev2.RemoteEventMessageRemove, evt.TS)
		c.queue(ctx, &simplevent.MessageRemove{EventMeta: meta, TargetMessage: networkid.MessageID(evt.MessageID)}, wait)
	case proto.EventReactionAdd, proto.EventReactionRemove:
		t := bridgev2.RemoteEventReaction
		if evt.Type == proto.EventReactionRemove {
			t = bridgev2.RemoteEventReactionRemove
		}
		meta, wait := c.meta(evt.ChatID, evt.UserID, t, evt.TS)
		c.queue(ctx, &simplevent.Reaction{
			EventMeta: meta, TargetMessage: networkid.MessageID(evt.MessageID), EmojiID: networkid.EmojiID(evt.Emoji), Emoji: evt.Emoji,
		}, wait)
	case proto.EventRead:
		meta, wait := c.meta(evt.ChatID, evt.UserID, bridgev2.RemoteEventReadReceipt, evt.TS)
		c.queue(ctx, &simplevent.Receipt{EventMeta: meta, LastTarget: networkid.MessageID(evt.MessageID)}, wait)
	case proto.EventTyping:
		meta, wait := c.meta(evt.ChatID, evt.UserID, bridgev2.RemoteEventTyping, evt.TS)
		timeout := time.Duration(0)
		if evt.Typing {
			timeout = 5 * time.Second
		}
		c.queue(ctx, &simplevent.Typing{EventMeta: meta, Timeout: timeout, Type: bridgev2.TypingTypeText}, wait)
	case proto.EventChatUpdate:
		if evt.Message != nil && evt.Message.System != nil {
			c.handleSystem(ctx, evt)
		} else if evt.Chat != nil {
			meta, wait := c.meta(evt.Chat.ID, "", bridgev2.RemoteEventChatResync, evt.TS)
			c.queue(ctx, &simplevent.ChatResync{EventMeta: meta, ChatInfo: c.chatInfo(*evt.Chat), LatestMessageTS: evt.Chat.LastMessageAt}, wait)
		}
	default:
		zerolog.Ctx(ctx).Debug().Str("type", evt.Type).Msg("Unknown FakeChat event")
	}
}

// handleSystem maps a system message (member joined or left, group renamed) to a
// chat info change, like the real connectors do.
func (c *Client) handleSystem(ctx context.Context, evt proto.Event) {
	m := evt.Message
	sys := m.System
	if sys == nil {
		return
	}
	change := &bridgev2.ChatInfoChange{}
	switch sys.Action {
	case proto.SystemMemberJoined, proto.SystemMemberLeft:
		membership := event.MembershipJoin
		if sys.Action == proto.SystemMemberLeft {
			membership = event.MembershipLeave
		}
		change.MemberChanges = &bridgev2.ChatMemberList{MemberMap: bridgev2.ChatMemberMap{}.Set(bridgev2.ChatMember{
			EventSender: c.sender(sys.UserID), Membership: membership,
		})}
	case proto.SystemRenamed:
		title := sys.Title
		change.ChatInfo = &bridgev2.ChatInfo{Name: &title}
	default:
		return
	}
	meta, wait := c.meta(m.ChatID, m.SenderID, bridgev2.RemoteEventChatInfoChange, m.TS)
	c.queue(ctx, &simplevent.ChatInfoChange{EventMeta: meta, ChatInfoChange: change}, wait)
}

func (c *Client) chatInfo(ch proto.Chat) *bridgev2.ChatInfo {
	title := ch.Title
	info := &bridgev2.ChatInfo{Name: &title, Members: &bridgev2.ChatMemberList{IsFull: true, MemberMap: bridgev2.ChatMemberMap{}}, CanBackfill: true}
	roomType := database.RoomTypeDefault
	if ch.Kind == "dm" {
		roomType = database.RoomTypeDM
	}
	info.Type = &roomType
	for _, id := range ch.Members {
		info.Members.MemberMap.Set(bridgev2.ChatMember{EventSender: c.sender(id), Membership: event.MembershipJoin})
		if ch.Kind == "dm" && id != c.self {
			info.Members.OtherUserID = networkid.UserID(id)
		}
	}
	if ch.AvatarMediaID != "" {
		mediaID := ch.AvatarMediaID
		info.Avatar = &bridgev2.Avatar{ID: networkid.AvatarID(mediaID), Get: func(ctx context.Context) ([]byte, error) { return c.api.Download(ctx, mediaID) }}
	}
	return info
}

func (c *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	ch, err := c.api.Chat(ctx, string(portal.ID))
	if err != nil {
		return nil, err
	}
	return c.chatInfo(ch), nil
}

func (c *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	u, err := c.api.User(ctx, string(ghost.ID))
	if err != nil {
		return nil, err
	}
	info := &bridgev2.UserInfo{Name: &u.Name}
	if u.Phone != "" {
		info.Identifiers = []string{"tel:" + u.Phone}
	}
	if u.AvatarMediaID != "" {
		mediaID := u.AvatarMediaID
		info.Avatar = &bridgev2.Avatar{ID: networkid.AvatarID(mediaID), Get: func(ctx context.Context) ([]byte, error) { return c.api.Download(ctx, mediaID) }}
	}
	return info, nil
}

func (c *Client) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return roomFeatures()
}
