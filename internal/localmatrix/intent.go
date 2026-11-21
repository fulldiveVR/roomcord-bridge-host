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
	"context"
	"fmt"
	"os"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Intent is the MatrixAPI of one puppet: the bridge bot, a remote-user ghost, or
// the Roomcord user (double puppet).
type Intent struct {
	c              *Connector
	mxid           id.UserID
	ghostID        networkid.UserID
	isBot          bool
	isDoublePuppet bool
}

func (in *Intent) GetMXID() id.UserID   { return in.mxid }
func (in *Intent) IsDoublePuppet() bool { return in.isDoublePuppet }

func (in *Intent) SendMessage(ctx context.Context, roomID id.RoomID, eventType event.Type, content *event.Content, extra *bridgev2.MatrixSendExtra) (*mautrix.RespSendEvent, error) {
	if extra == nil {
		extra = &bridgev2.MatrixSendExtra{}
	}
	var evtID id.EventID
	switch {
	case extra.MessageMeta != nil && eventType != event.EventRedaction && eventType != event.EventReaction:
		evtID = messageEventID(roomID, extra.MessageMeta.ID, extra.MessageMeta.PartID)
	case extra.ReactionMeta != nil && eventType == event.EventReaction:
		evtID = reactionEventID(roomID, extra.ReactionMeta.MessageID, extra.ReactionMeta.MessagePartID, extra.ReactionMeta.SenderID, extra.ReactionMeta.EmojiID)
	default:
		evtID = randomEventID()
	}
	if err := in.c.translate(ctx, in, roomID, evtID, eventType, content, extra); err != nil {
		return nil, err
	}
	return &mautrix.RespSendEvent{EventID: evtID}, nil
}

func (in *Intent) SendState(ctx context.Context, roomID id.RoomID, eventType event.Type, stateKey string, content *event.Content, ts time.Time) (*mautrix.RespSendEvent, error) {
	if err := in.c.translateState(ctx, roomID, eventType, stateKey, content); err != nil {
		return nil, err
	}
	return &mautrix.RespSendEvent{EventID: randomEventID()}, nil
}

func (in *Intent) MarkRead(ctx context.Context, roomID id.RoomID, eventID id.EventID, ts time.Time) error {
	return in.c.translateReceipt(ctx, in, roomID, eventID)
}

func (in *Intent) MarkUnread(ctx context.Context, roomID id.RoomID, unread bool) error { return nil }

func (in *Intent) MarkTyping(ctx context.Context, roomID id.RoomID, typingType bridgev2.TypingType, timeout time.Duration) error {
	return nil
}

func (in *Intent) DownloadMedia(ctx context.Context, uri id.ContentURIString, file *event.EncryptedFileInfo) ([]byte, error) {
	if file != nil {
		uri = file.URL
	}
	path, err := in.c.mediaPath(uri)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (in *Intent) DownloadMediaToFile(ctx context.Context, uri id.ContentURIString, file *event.EncryptedFileInfo, writable bool, callback func(*os.File) error) error {
	if file != nil {
		uri = file.URL
	}
	path, err := in.c.mediaPath(uri)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "bridge-host-media-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err = tmp.Write(src); err != nil {
		return err
	}
	if _, err = tmp.Seek(0, 0); err != nil {
		return err
	}
	return callback(tmp)
}

func (in *Intent) UploadMedia(ctx context.Context, roomID id.RoomID, data []byte, fileName, mimeType string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	uri, err := in.c.storeMedia(ctx, roomID, data, fileName, mimeType)
	return uri, nil, err
}

func (in *Intent) UploadMediaStream(ctx context.Context, roomID id.RoomID, size int64, requireFile bool, cb bridgev2.FileStreamCallback) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	tmp, err := os.CreateTemp("", "bridge-host-upload-*")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	res, err := cb(tmp)
	if err != nil {
		return "", nil, bridgev2.CallbackError{Type: "write", Wrapped: err}
	}
	path := tmp.Name()
	if res.ReplacementFile != "" {
		path = res.ReplacementFile
		defer func() { _ = os.Remove(res.ReplacementFile) }()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	uri, err := in.c.storeMedia(ctx, roomID, data, res.FileName, res.MimeType)
	return uri, nil, err
}

func (in *Intent) SetDisplayName(ctx context.Context, name string) error                 { return nil }
func (in *Intent) SetAvatarURL(ctx context.Context, avatarURL id.ContentURIString) error { return nil }
func (in *Intent) SetExtraProfileMeta(ctx context.Context, data any) error               { return nil }
func (in *Intent) SetProfile(ctx context.Context, data any) error                        { return nil }

func (in *Intent) CreateRoom(ctx context.Context, req *mautrix.ReqCreateRoom) (id.RoomID, error) {
	roomID := req.BeeperLocalRoomID
	if roomID == "" {
		return "", fmt.Errorf("%w: room creation without a deterministic room id", ErrNotImplemented)
	}
	if err := in.c.translateCreateRoom(ctx, roomID, req); err != nil {
		return "", err
	}
	return roomID, nil
}

func (in *Intent) DeleteRoom(ctx context.Context, roomID id.RoomID, puppetsOnly bool) error {
	if puppetsOnly {
		return nil
	}
	in.c.roomsMu.Lock()
	delete(in.c.rooms, roomID)
	in.c.roomsMu.Unlock()
	return nil
}

func (in *Intent) EnsureJoined(ctx context.Context, roomID id.RoomID, params ...bridgev2.EnsureJoinedParams) error {
	in.c.setMember(roomID, in.mxid, &event.MemberEventContent{Membership: event.MembershipJoin})
	return nil
}

func (in *Intent) EnsureInvited(ctx context.Context, roomID id.RoomID, userID id.UserID) error {
	in.c.setMember(roomID, userID, &event.MemberEventContent{Membership: event.MembershipJoin})
	return nil
}

func (in *Intent) TagRoom(ctx context.Context, roomID id.RoomID, tag event.RoomTag, isTagged bool) error {
	return nil
}

func (in *Intent) MuteRoom(ctx context.Context, roomID id.RoomID, until time.Time) error { return nil }

func (in *Intent) GetEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*event.Event, error) {
	return nil, fmt.Errorf("%w: GetEvent", ErrNotImplemented)
}

func (c *Connector) setMember(roomID id.RoomID, userID id.UserID, content *event.MemberEventContent) {
	r := c.room(roomID)
	c.roomsMu.Lock()
	defer c.roomsMu.Unlock()
	if content.Membership == event.MembershipLeave || content.Membership == event.MembershipBan {
		delete(r.members, userID)
		return
	}
	cp := *content
	r.members[userID] = &cp
}
