// roomcord-bridge-host - A host process for the mautrix bridgev2 network connectors.
// Copyright (C) 2026 The roomcord-bridge-host authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package localmatrix implements the Matrix side of bridgev2 (MatrixConnector and
// MatrixAPI) without a homeserver. Every call the core makes towards "Matrix" is
// translated into a Roomcord wire event on the owning login's outbox.
package localmatrix

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/store"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

var ErrNotImplemented = errors.New("not implemented by the bridge host")

type room struct {
	members  map[id.UserID]*event.MemberEventContent
	power    *event.PowerLevelsEventContent
	isDirect bool
	isSpace  bool
}

type healthState struct {
	health wire.Health
	status string
}

type Connector struct {
	Network  string
	Store    *store.Store
	MediaDir string
	Log      zerolog.Logger

	br  *bridgev2.Bridge
	bot *Intent

	roomsMu sync.Mutex
	rooms   map[id.RoomID]*room

	waitersMu sync.Mutex
	waiters   map[id.EventID]chan *StatusUpdate

	healthMu sync.Mutex
	health   map[string]*healthState

	router *http.ServeMux
}

var (
	_ bridgev2.MatrixConnector           = (*Connector)(nil)
	_ bridgev2.MatrixConnectorWithServer = (*Connector)(nil)
	_ bridgev2.MatrixAPI                 = (*Intent)(nil)
)

// GetPublicAddress and GetRouter satisfy MatrixConnectorWithServer: beeper/line refuses to start without it but never
// calls either method. The router is never served, so a connector that mounts a route there gets no traffic.
func (c *Connector) GetPublicAddress() string  { return "" }
func (c *Connector) GetRouter() *http.ServeMux { return c.router }

func New(network string, st *store.Store, mediaDir string, log zerolog.Logger) *Connector {
	return &Connector{
		Network:  network,
		Store:    st,
		MediaDir: mediaDir,
		Log:      log,
		rooms:    make(map[id.RoomID]*room),
		waiters:  make(map[id.EventID]chan *StatusUpdate),
		health:   make(map[string]*healthState),
		router:   http.NewServeMux(),
	}
}

func (c *Connector) Bridge() *bridgev2.Bridge { return c.br }

// LoginKey is the wire loginId: user login ids are only unique within one network.
func (c *Connector) LoginKey(loginID networkid.UserLoginID) string {
	return c.Network + ":" + string(loginID)
}

func (c *Connector) Init(br *bridgev2.Bridge) {
	c.br = br
	c.bot = &Intent{c: c, mxid: botMXID, isBot: true}
}

func (c *Connector) BotIntent() bridgev2.MatrixAPI { return c.bot }

func (c *Connector) Start(ctx context.Context) error { return nil }
func (c *Connector) PreStop()                        {}
func (c *Connector) Stop()                           {}

func (c *Connector) GetCapabilities() *bridgev2.MatrixCapabilities {
	return &bridgev2.MatrixCapabilities{AutoJoinInvites: true, BatchSending: true}
}

func (c *Connector) ServerName() string { return ServerName }

func (c *Connector) ParseGhostMXID(userID id.UserID) (networkid.UserID, bool) {
	return parseGhostMXID(userID)
}

func (c *Connector) FormatGhostMXID(userID networkid.UserID) id.UserID { return ghostMXID(userID) }

func (c *Connector) GhostIntent(userID networkid.UserID) bridgev2.MatrixAPI {
	return &Intent{c: c, mxid: ghostMXID(userID), ghostID: userID}
}

// NewUserIntent returns a "double puppet" for every Roomcord user: messages the
// user sent from another device are then attributed to the user, not to a ghost.
func (c *Connector) NewUserIntent(ctx context.Context, userID id.UserID, accessToken string) (bridgev2.MatrixAPI, string, error) {
	if _, ok := ParseUserMXID(userID); !ok {
		return nil, accessToken, fmt.Errorf("not a local user: %s", userID)
	}
	return &Intent{c: c, mxid: userID, isDoublePuppet: true}, accessToken, nil
}

func (c *Connector) SendBridgeStatus(ctx context.Context, state *status.BridgeState) error {
	if state.RemoteID == "" {
		return nil
	}
	loginKey := c.LoginKey(state.RemoteID)
	st, code := MapBridgeState(state.StateEvent)
	c.healthMu.Lock()
	prev := c.health[loginKey]
	changed := prev == nil || prev.status != st
	detail := state.Message
	if detail == "" && state.Error != "" {
		detail = string(state.Error)
	}
	if changed {
		c.health[loginKey] = &healthState{status: st, health: wire.Health{State: st, Since: time.Now().UTC(), Detail: detail}}
	} else {
		prev.health.Detail = detail
	}
	c.healthMu.Unlock()
	if !changed {
		return nil
	}
	payload := &wire.AccountStatusPayload{Status: st, Detail: detail}
	if code != "" {
		payload.Code = &code
	}
	_, err := c.Store.Emit(ctx, loginKey, wire.EventAccountStatus, payload)
	return err
}

// MapBridgeState maps a bridgev2 bridge state to the wire account state and error code.
func MapBridgeState(evt status.BridgeStateEvent) (state string, code string) {
	switch evt {
	case status.StateConnected, status.StateBackfilling, status.StateRunning:
		return "connected", ""
	case status.StateConnecting, status.StateTransientDisconnect, status.StateStarting:
		return "connecting", ""
	case status.StateBadCredentials:
		return "needs_reauth", "session_revoked"
	case status.StateLoggedOut:
		return "needs_reauth", "session_revoked"
	case status.StateUnknownError:
		return "error", "network_unavailable"
	default:
		return "error", "internal_error"
	}
}

func (c *Connector) Health(loginKey string) (wire.Health, bool) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	h, ok := c.health[loginKey]
	if !ok {
		return wire.Health{}, false
	}
	return h.health, true
}

func (c *Connector) ForgetHealth(loginKey string) {
	c.healthMu.Lock()
	delete(c.health, loginKey)
	c.healthMu.Unlock()
}

type StatusUpdate struct {
	Status bridgev2.MessageStatus
	Info   bridgev2.MessageStatusEventInfo
}

// WaitForStatus registers interest in the message status of an outgoing event. It
// must be called before the event is queued.
func (c *Connector) WaitForStatus(evtID id.EventID) (<-chan *StatusUpdate, func()) {
	ch := make(chan *StatusUpdate, 4)
	c.waitersMu.Lock()
	c.waiters[evtID] = ch
	c.waitersMu.Unlock()
	return ch, func() {
		c.waitersMu.Lock()
		if c.waiters[evtID] == ch {
			delete(c.waiters, evtID)
		}
		c.waitersMu.Unlock()
	}
}

func (c *Connector) SendMessageStatus(ctx context.Context, ms *bridgev2.MessageStatus, info *bridgev2.MessageStatusEventInfo) {
	c.waitersMu.Lock()
	ch := c.waiters[info.SourceEventID]
	c.waitersMu.Unlock()
	if ch == nil {
		zerolog.Ctx(ctx).Debug().Stringer("event_id", info.SourceEventID).Str("status", string(ms.Status)).Msg("Message status without waiter")
		return
	}
	select {
	case ch <- &StatusUpdate{Status: *ms, Info: *info}:
	default:
	}
}

func (c *Connector) GenerateContentURI(ctx context.Context, mediaID networkid.MediaID) (id.ContentURIString, error) {
	return id.ContentURIString("mxc://" + ServerName + "/direct_" + b64.EncodeToString(mediaID)), nil
}

func (c *Connector) ParseContentURI(ctx context.Context, contentURI id.ContentURIString) (networkid.MediaID, error) {
	parsed, err := contentURI.Parse()
	if err != nil {
		return nil, err
	}
	raw, ok := strings.CutPrefix(parsed.FileID, "direct_")
	if !ok {
		return nil, fmt.Errorf("not a direct media uri")
	}
	return b64.DecodeString(raw)
}

func (c *Connector) room(roomID id.RoomID) *room {
	c.roomsMu.Lock()
	defer c.roomsMu.Unlock()
	r, ok := c.rooms[roomID]
	if !ok {
		r = &room{members: make(map[id.UserID]*event.MemberEventContent)}
		c.rooms[roomID] = r
	}
	return r
}

func (c *Connector) GetPowerLevels(ctx context.Context, roomID id.RoomID) (*event.PowerLevelsEventContent, error) {
	r := c.room(roomID)
	c.roomsMu.Lock()
	defer c.roomsMu.Unlock()
	if r.power == nil {
		r.power = &event.PowerLevelsEventContent{Users: map[id.UserID]int{botMXID: 9001}}
	}
	return r.power, nil
}

func (c *Connector) GetMembers(ctx context.Context, roomID id.RoomID) (map[id.UserID]*event.MemberEventContent, error) {
	r := c.room(roomID)
	c.roomsMu.Lock()
	defer c.roomsMu.Unlock()
	out := make(map[id.UserID]*event.MemberEventContent, len(r.members))
	for k, v := range r.members {
		cp := *v
		out[k] = &cp
	}
	return out, nil
}

func (c *Connector) GetMemberInfo(ctx context.Context, roomID id.RoomID, userID id.UserID) (*event.MemberEventContent, error) {
	r := c.room(roomID)
	c.roomsMu.Lock()
	defer c.roomsMu.Unlock()
	if m, ok := r.members[userID]; ok {
		cp := *m
		return &cp, nil
	}
	return nil, nil
}

func (c *Connector) BatchSend(ctx context.Context, roomID id.RoomID, req *mautrix.ReqBeeperBatchSend, extras []*bridgev2.MatrixSendExtra) (*mautrix.RespBeeperBatchSend, error) {
	resp := &mautrix.RespBeeperBatchSend{EventIDs: make([]id.EventID, len(req.Events))}
	for i, evt := range req.Events {
		var extra *bridgev2.MatrixSendExtra
		if i < len(extras) {
			extra = extras[i]
		}
		if extra == nil {
			extra = &bridgev2.MatrixSendExtra{}
		}
		if extra.Timestamp.IsZero() && evt.Timestamp != 0 {
			extra.Timestamp = time.UnixMilli(evt.Timestamp)
		}
		evtID := evt.ID
		if evtID == "" {
			evtID = randomEventID()
		}
		sender := c.intentFor(evt.Sender)
		if err := c.translate(ctx, sender, roomID, evtID, evt.Type, &evt.Content, extra); err != nil {
			return nil, err
		}
		resp.EventIDs[i] = evtID
	}
	return resp, nil
}

func (c *Connector) intentFor(userID id.UserID) *Intent {
	if userID == botMXID {
		return c.bot
	}
	if ghost, ok := parseGhostMXID(userID); ok {
		return &Intent{c: c, mxid: userID, ghostID: ghost}
	}
	return &Intent{c: c, mxid: userID, isDoublePuppet: true}
}

func (c *Connector) GenerateDeterministicRoomID(portalKey networkid.PortalKey) id.RoomID {
	return RoomID(portalKey)
}

func (c *Connector) GenerateDeterministicEventID(roomID id.RoomID, portalKey networkid.PortalKey, messageID networkid.MessageID, partID networkid.PartID) id.EventID {
	return messageEventID(roomID, messageID, partID)
}

func (c *Connector) GenerateReactionEventID(roomID id.RoomID, targetMessage *database.Message, sender networkid.UserID, emojiID networkid.EmojiID) id.EventID {
	return reactionEventID(roomID, targetMessage.ID, targetMessage.PartID, sender, emojiID)
}
