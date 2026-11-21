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

package localmatrix

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/xid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/store"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

func parseContent(evtType event.Type, content *event.Content) {
	if content.Parsed == nil && content.VeryRaw != nil {
		_ = content.ParseRaw(evtType)
	}
}

func (c *Connector) routing(roomID id.RoomID) (networkid.PortalKey, string, bool) {
	key, ok := ParseRoomID(roomID)
	if !ok || key.Receiver == "" {
		c.Log.Debug().Stringer("room_id", roomID).Msg("Dropping event for room without a receiver login")
		return key, "", false
	}
	return key, c.LoginKey(key.Receiver), true
}

func (c *Connector) login(receiver networkid.UserLoginID) *bridgev2.UserLogin {
	return c.br.GetCachedUserLoginByID(receiver)
}

// SelfRemoteID is the network user id of the login's own account.
func (c *Connector) SelfRemoteID(receiver networkid.UserLoginID) networkid.UserID {
	if login := c.login(receiver); login != nil && login.Client != nil {
		if withID, ok := login.Client.(bridgev2.NetworkAPIWithUserID); ok {
			return withID.GetUserID()
		}
	}
	return networkid.UserID(receiver)
}

func (c *Connector) isThisUser(ctx context.Context, receiver networkid.UserLoginID, userID networkid.UserID) bool {
	if login := c.login(receiver); login != nil && login.Client != nil {
		return login.Client.IsThisUser(ctx, userID)
	}
	return false
}

func (c *Connector) ghostName(ctx context.Context, userID networkid.UserID) *string {
	ghost, err := c.br.DB.Ghost.GetByID(ctx, userID)
	if err != nil || ghost == nil || ghost.Name == "" {
		return nil
	}
	return &ghost.Name
}

func (c *Connector) participant(ctx context.Context, receiver networkid.UserLoginID, userID id.UserID) (wire.Participant, bool) {
	p := wire.Participant{Network: c.Network}
	if ghostID, ok := parseGhostMXID(userID); ok {
		p.ExternalParticipantID = string(ghostID)
		p.DisplayName = ptr.Val(c.ghostName(ctx, ghostID))
		p.IsSelf = c.isThisUser(ctx, receiver, ghostID)
		return p, true
	}
	if _, ok := ParseUserMXID(userID); ok {
		p.ExternalParticipantID = string(c.SelfRemoteID(receiver))
		p.IsSelf = true
		if login := c.login(receiver); login != nil {
			p.DisplayName = login.RemoteName
		}
		return p, true
	}
	return p, false
}

func (c *Connector) conversation(ctx context.Context, key networkid.PortalKey, roomID id.RoomID, title *string) wire.Conversation {
	conv := wire.Conversation{
		Network:                c.Network,
		ExternalConversationID: string(key.ID),
		Kind:                   "group",
		ParticipantExternalIDs: []string{},
	}
	r := c.room(roomID)
	c.roomsMu.Lock()
	if r.isDirect {
		conv.Kind = "dm"
	}
	members := make([]id.UserID, 0, len(r.members))
	for userID := range r.members {
		members = append(members, userID)
	}
	c.roomsMu.Unlock()
	if portal, err := c.br.DB.Portal.GetByKey(ctx, key); err == nil && portal != nil {
		conv.Title = portal.Name
		if portal.RoomType == database.RoomTypeDM {
			conv.Kind = "dm"
		}
	}
	if title != nil {
		conv.Title = *title
	}
	for _, userID := range members {
		if p, ok := c.participant(ctx, key.Receiver, userID); ok {
			conv.ParticipantExternalIDs = append(conv.ParticipantExternalIDs, p.ExternalParticipantID)
		}
	}
	return conv
}

func (c *Connector) translateCreateRoom(ctx context.Context, roomID id.RoomID, req *mautrix.ReqCreateRoom) error {
	key, loginKey, ok := c.routing(roomID)
	r := c.room(roomID)
	var title *string
	c.roomsMu.Lock()
	r.isDirect = req.IsDirect
	r.isSpace = req.CreationContent["type"] == event.RoomTypeSpace
	if req.PowerLevelOverride != nil {
		r.power = req.PowerLevelOverride
	}
	for _, userID := range append(append([]id.UserID{}, req.BeeperInitialMembers...), req.Invite...) {
		r.members[userID] = &event.MemberEventContent{Membership: event.MembershipJoin}
	}
	isSpace := r.isSpace
	c.roomsMu.Unlock()
	for _, evt := range req.InitialState {
		if evt.Type == event.StateRoomName {
			parseContent(evt.Type, &evt.Content)
			if name, ok := evt.Content.Parsed.(*event.RoomNameEventContent); ok {
				title = &name.Name
			}
		}
	}
	if !ok || isSpace {
		return nil
	}
	conv := c.conversation(ctx, key, roomID, title)
	payload := &wire.ConversationUpsertPayload{Conversation: conv}
	seen := map[string]bool{}
	for _, userID := range append(append([]id.UserID{}, req.BeeperInitialMembers...), req.Invite...) {
		if p, ok := c.participant(ctx, key.Receiver, userID); ok && !seen[p.ExternalParticipantID] {
			seen[p.ExternalParticipantID] = true
			payload.Participants = append(payload.Participants, p)
		}
	}
	_, err := c.Store.Emit(ctx, loginKey, wire.EventConversationUpsert, payload)
	return err
}

func (c *Connector) translateState(ctx context.Context, roomID id.RoomID, evtType event.Type, stateKey string, content *event.Content) error {
	parseContent(evtType, content)
	key, loginKey, routable := c.routing(roomID)
	switch evtType {
	case event.StateMember:
		member, ok := content.Parsed.(*event.MemberEventContent)
		if !ok {
			return nil
		}
		userID := id.UserID(stateKey)
		c.setMember(roomID, userID, member)
		if !routable || (member.Membership != event.MembershipJoin && member.Membership != event.MembershipInvite) {
			return nil
		}
		p, ok := c.participant(ctx, key.Receiver, userID)
		if !ok {
			return nil
		}
		if member.Displayname != "" {
			p.DisplayName = member.Displayname
		}
		_, err := c.Store.Emit(ctx, loginKey, wire.EventParticipantUpsert, &wire.ParticipantUpsertPayload{
			ExternalConversationID: string(key.ID),
			Participant:            p,
		})
		return err
	case event.StatePowerLevels:
		if pl, ok := content.Parsed.(*event.PowerLevelsEventContent); ok {
			r := c.room(roomID)
			c.roomsMu.Lock()
			r.power = pl
			c.roomsMu.Unlock()
		}
		return nil
	case event.StateRoomName:
		name, ok := content.Parsed.(*event.RoomNameEventContent)
		if !ok || !routable {
			return nil
		}
		conv := c.conversation(ctx, key, roomID, &name.Name)
		_, err := c.Store.Emit(ctx, loginKey, wire.EventConversationUpsert, &wire.ConversationUpsertPayload{Conversation: conv})
		return err
	default:
		return nil
	}
}

func (c *Connector) translateReceipt(ctx context.Context, in *Intent, roomID id.RoomID, eventID id.EventID) error {
	key, loginKey, ok := c.routing(roomID)
	if !ok {
		return nil
	}
	msg, err := c.br.DB.Message.GetPartByMXID(ctx, eventID)
	if err != nil || msg == nil {
		return err
	}
	payload := &wire.ReadReceiptPayload{
		ExternalConversationID:    string(key.ID),
		LastReadExternalMessageID: ExternalMessageID(msg.ID, msg.PartID),
	}
	switch {
	case in.isDoublePuppet:
		payload.ReaderExternalID = string(c.SelfRemoteID(key.Receiver))
		payload.IsSelf = true
	case in.ghostID != "":
		payload.ReaderExternalID = string(in.ghostID)
		payload.IsSelf = c.isThisUser(ctx, key.Receiver, in.ghostID)
	default:
		return nil
	}
	_, err = c.Store.Emit(ctx, loginKey, wire.EventReadReceipt, payload)
	return err
}

func (c *Connector) translate(ctx context.Context, in *Intent, roomID id.RoomID, evtID id.EventID, evtType event.Type, content *event.Content, extra *bridgev2.MatrixSendExtra) error {
	key, loginKey, ok := c.routing(roomID)
	if !ok {
		return nil
	}
	parseContent(evtType, content)
	ts := extra.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()
	convID := string(key.ID)
	switch evtType {
	case event.EventMessage, event.EventSticker:
		msgContent, ok := content.Parsed.(*event.MessageEventContent)
		if !ok {
			return nil
		}
		if msgContent.RelatesTo != nil && msgContent.RelatesTo.Type == event.RelReplace {
			return c.translateEdit(ctx, key, loginKey, msgContent, ts)
		}
		meta := extra.MessageMeta
		if meta == nil {
			zerolog.Ctx(ctx).Debug().Stringer("event_id", evtID).Msg("Message without message metadata, not bridged")
			return nil
		}
		fromMe := in.isDoublePuppet || c.isThisUser(ctx, key.Receiver, meta.SenderID)
		msg := c.buildMessage(ctx, key.Receiver, convID, ExternalMessageID(meta.ID, meta.PartID), meta.SenderID, fromMe, evtType, msgContent, ts)
		if meta.ReplyTo.MessageID != "" {
			reply := ExternalMessageID(meta.ReplyTo.MessageID, ptr.Val(meta.ReplyTo.PartID))
			msg.ReplyToExternalMessageID = &reply
		}
		_, err := c.Store.Emit(ctx, loginKey, wire.EventMessageUpsert, &wire.MessageUpsertPayload{Message: msg})
		return err
	case event.EventReaction:
		reaction, ok := content.Parsed.(*event.ReactionEventContent)
		meta := extra.ReactionMeta
		if !ok || meta == nil {
			return nil
		}
		emoji := meta.Emoji
		if emoji == "" {
			emoji = reaction.RelatesTo.Key
		}
		_, err := c.Store.Emit(ctx, loginKey, wire.EventReactionUpsert, &wire.ReactionPayload{
			ExternalConversationID: convID,
			ExternalMessageID:      ExternalMessageID(meta.MessageID, meta.MessagePartID),
			Reaction:               wire.Reaction{SenderExternalID: string(meta.SenderID), Emoji: emoji, ReactedAt: &ts},
		})
		return err
	case event.EventRedaction:
		redaction, ok := content.Parsed.(*event.RedactionEventContent)
		if !ok || redaction.Redacts == "" {
			return nil
		}
		if msg, err := c.br.DB.Message.GetPartByMXID(ctx, redaction.Redacts); err != nil {
			return err
		} else if msg != nil {
			_, err = c.Store.Emit(ctx, loginKey, wire.EventMessageDelete, &wire.MessageDeletePayload{
				ExternalConversationID: convID,
				ExternalMessageID:      ExternalMessageID(msg.ID, msg.PartID),
				DeletedAt:              ts,
			})
			return err
		}
		if reaction, err := c.br.DB.Reaction.GetByMXID(ctx, redaction.Redacts); err != nil {
			return err
		} else if reaction != nil {
			_, err = c.Store.Emit(ctx, loginKey, wire.EventReactionDelete, &wire.ReactionPayload{
				ExternalConversationID: convID,
				ExternalMessageID:      ExternalMessageID(reaction.MessageID, reaction.MessagePartID),
				Reaction:               wire.Reaction{Network: c.Network, SenderExternalID: string(reaction.SenderID), Emoji: cmp.Or(reaction.Emoji, string(reaction.EmojiID))},
			})
			return err
		}
		return nil
	default:
		zerolog.Ctx(ctx).Debug().Str("event_type", evtType.Type).Msg("Event type not translated to the wire")
		return nil
	}
}

func (c *Connector) translateEdit(ctx context.Context, key networkid.PortalKey, loginKey string, content *event.MessageEventContent, ts time.Time) error {
	target, err := c.br.DB.Message.GetPartByMXID(ctx, content.RelatesTo.EventID)
	if err != nil || target == nil {
		return err
	}
	newContent := content.NewContent
	if newContent == nil {
		cp := *content
		cp.Body = strings.TrimPrefix(cp.Body, "* ")
		newContent = &cp
	}
	fromMe := target.IsDoublePuppeted || c.isThisUser(ctx, key.Receiver, target.SenderID)
	msg := c.buildMessage(ctx, key.Receiver, string(key.ID), ExternalMessageID(target.ID, target.PartID), target.SenderID, fromMe, event.EventMessage, newContent, target.Timestamp.UTC())
	msg.EditedAt = &ts
	if target.ReplyTo.MessageID != "" {
		reply := ExternalMessageID(target.ReplyTo.MessageID, ptr.Val(target.ReplyTo.PartID))
		msg.ReplyToExternalMessageID = &reply
	}
	_, err = c.Store.Emit(ctx, loginKey, wire.EventMessageUpsert, &wire.MessageUpsertPayload{Message: msg})
	return err
}

func (c *Connector) buildMessage(
	ctx context.Context, receiver networkid.UserLoginID, convID, extID string, senderID networkid.UserID, fromMe bool,
	evtType event.Type, content *event.MessageEventContent, sentAt time.Time,
) wire.Message {
	msg := wire.Message{
		Network:                c.Network,
		ExternalConversationID: convID,
		ExternalMessageID:      extID,
		Direction:              "inbound",
		SenderExternalID:       string(senderID),
		SenderName:             c.ghostName(ctx, senderID),
		Attachments:            []wire.Attachment{},
		Reactions:              []wire.Reaction{},
		Status:                 "delivered",
		SentAt:                 sentAt,
	}
	if fromMe {
		msg.Direction = "outbound"
		msg.Status = "sent"
		if login := c.login(receiver); login != nil && msg.SenderName == nil {
			msg.SenderName = &login.RemoteName
		}
	}
	kind := ""
	switch {
	case evtType == event.EventSticker || content.MsgType == event.CapMsgSticker:
		kind = "sticker"
	case content.MsgType == event.MsgImage:
		kind = "image"
	case content.MsgType == event.MsgVideo:
		kind = "video"
	case content.MsgType == event.MsgAudio && content.MSC3245Voice != nil:
		kind = "voice"
	case content.MsgType == event.MsgAudio:
		kind = "audio"
	case content.MsgType == event.MsgFile:
		kind = "file"
	}
	if kind == "" {
		msg.Text = content.Body
		return msg
	}
	if content.FileName != "" && content.Body != content.FileName {
		msg.Text = content.Body
	}
	uri := content.URL
	if content.File != nil {
		uri = content.File.URL
	}
	att := wire.Attachment{Network: c.Network, Kind: kind, MimeType: "application/octet-stream"}
	if parsed, err := uri.Parse(); err == nil {
		att.ExternalAttachmentID = parsed.FileID
	}
	name := content.FileName
	if name == "" {
		name = content.Body
	}
	if name != "" {
		att.FileName = &name
	}
	if info := content.Info; info != nil {
		if info.MimeType != "" {
			att.MimeType = info.MimeType
		}
		if info.Size > 0 {
			att.SizeBytes = ptr.Ptr(int64(info.Size))
		}
		if info.Width > 0 {
			att.Width = ptr.Ptr(info.Width)
		}
		if info.Height > 0 {
			att.Height = ptr.Ptr(info.Height)
		}
		if info.Duration > 0 {
			att.DurationMs = ptr.Ptr(info.Duration)
		}
	}
	msg.Attachments = append(msg.Attachments, att)
	return msg
}

func (c *Connector) mediaPath(uri id.ContentURIString) (string, error) {
	parsed, err := uri.Parse()
	if err != nil {
		return "", err
	}
	if parsed.Homeserver != ServerName || strings.ContainsAny(parsed.FileID, "/\\.") {
		return "", fmt.Errorf("unknown media %s", uri)
	}
	return filepath.Join(c.MediaDir, parsed.FileID), nil
}

func (c *Connector) storeMedia(ctx context.Context, roomID id.RoomID, data []byte, fileName, mimeType string) (id.ContentURIString, error) {
	return StoreMedia(ctx, c.Store, c.MediaDir, c.loginKeyForRoom(roomID), data, fileName, mimeType)
}

func (c *Connector) loginKeyForRoom(roomID id.RoomID) string {
	if key, ok := ParseRoomID(roomID); ok && key.Receiver != "" {
		return c.LoginKey(key.Receiver)
	}
	return ""
}

// StoreMedia writes a file into the media dir and returns its mxc uri.
func StoreMedia(ctx context.Context, st *store.Store, mediaDir, loginKey string, data []byte, fileName, mimeType string) (id.ContentURIString, error) {
	mediaID := xid.New().String()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(mediaDir, mediaID), data, 0o600); err != nil {
		return "", err
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if err := st.PutMedia(ctx, &store.Media{ID: mediaID, LoginID: loginKey, MimeType: mimeType, FileName: fileName, Size: int64(len(data))}); err != nil {
		return "", err
	}
	return id.ContentURIString("mxc://" + ServerName + "/" + mediaID), nil
}

// OutgoingMessage builds the wire message for a message the host itself sent.
func (c *Connector) OutgoingMessage(ctx context.Context, receiver networkid.UserLoginID, convID string, msg *database.Message, content *event.MessageEventContent) wire.Message {
	out := c.buildMessage(ctx, receiver, convID, ExternalMessageID(msg.ID, msg.PartID), msg.SenderID, true, event.EventMessage, content, msg.Timestamp.UTC())
	if msg.ReplyTo.MessageID != "" {
		reply := ExternalMessageID(msg.ReplyTo.MessageID, ptr.Val(msg.ReplyTo.PartID))
		out.ReplyToExternalMessageID = &reply
	}
	return out
}
