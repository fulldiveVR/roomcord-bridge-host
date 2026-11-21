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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/rs/xid"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// Users, ghosts and the bot live on different fake server names so that no
// Roomcord user id can ever collide with a ghost or the bot.
const (
	ServerName      = "roomcord.local"
	ghostServerName = "ghost.roomcord.local"
	botMXID         = id.UserID("@bridgebot:bot.roomcord.local")
)

var b64 = base64.RawURLEncoding

func UserMXID(userID string) id.UserID {
	return id.NewUserID(id.EncodeUserLocalpart(userID), ServerName)
}

func ParseUserMXID(mxid id.UserID) (string, bool) {
	localpart, server, err := mxid.Parse()
	if err != nil || server != ServerName {
		return "", false
	}
	decoded, err := id.DecodeUserLocalpart(localpart)
	if err != nil {
		return "", false
	}
	return decoded, true
}

func ghostMXID(userID networkid.UserID) id.UserID {
	return id.NewUserID(id.EncodeUserLocalpart(string(userID)), ghostServerName)
}

func parseGhostMXID(mxid id.UserID) (networkid.UserID, bool) {
	localpart, server, err := mxid.Parse()
	if err != nil || server != ghostServerName {
		return "", false
	}
	decoded, err := id.DecodeUserLocalpart(localpart)
	if err != nil {
		return "", false
	}
	return networkid.UserID(decoded), true
}

// RoomID encodes the portal key into the room id, so every Matrix-side call can be
// routed to its login (with split portals the receiver is the login) without a lookup.
func RoomID(key networkid.PortalKey) id.RoomID {
	return id.RoomID("!" + b64.EncodeToString([]byte(key.ID)) + "." + b64.EncodeToString([]byte(key.Receiver)) + ":" + ServerName)
}

func ParseRoomID(roomID id.RoomID) (networkid.PortalKey, bool) {
	s, ok := strings.CutPrefix(string(roomID), "!")
	if !ok {
		return networkid.PortalKey{}, false
	}
	s, ok = strings.CutSuffix(s, ":"+ServerName)
	if !ok {
		return networkid.PortalKey{}, false
	}
	idPart, receiverPart, ok := strings.Cut(s, ".")
	if !ok {
		return networkid.PortalKey{}, false
	}
	portalID, err1 := b64.DecodeString(idPart)
	receiver, err2 := b64.DecodeString(receiverPart)
	if err1 != nil || err2 != nil {
		return networkid.PortalKey{}, false
	}
	return networkid.PortalKey{ID: networkid.PortalID(portalID), Receiver: networkid.UserLoginID(receiver)}, true
}

func hashEventID(parts ...string) id.EventID {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return id.EventID("$" + hex.EncodeToString(h.Sum(nil)[:16]) + ":" + ServerName)
}

func messageEventID(roomID id.RoomID, msgID networkid.MessageID, partID networkid.PartID) id.EventID {
	return hashEventID("m", string(roomID), string(msgID), string(partID))
}

func reactionEventID(roomID id.RoomID, msgID networkid.MessageID, partID networkid.PartID, sender networkid.UserID, emoji networkid.EmojiID) id.EventID {
	return hashEventID("r", string(roomID), string(msgID), string(partID), string(sender), string(emoji))
}

func randomEventID() id.EventID {
	return id.EventID("$" + xid.New().String() + ":" + ServerName)
}

// OutgoingEventID is the event id of a send request. It is deterministic per
// (login, clientMessageId), which is what makes a repeated send a no-op in the core.
func OutgoingEventID(loginID, clientMessageID string) id.EventID {
	return hashEventID("out", loginID, clientMessageID)
}

// ExternalMessageID is the wire id of one message part. Single-part messages keep
// the network message id as is.
func ExternalMessageID(msgID networkid.MessageID, partID networkid.PartID) string {
	if partID == "" {
		return string(msgID)
	}
	return string(msgID) + "#" + string(partID)
}

func SplitExternalMessageID(ext string) (networkid.MessageID, networkid.PartID) {
	if msg, part, ok := strings.Cut(ext, "#"); ok {
		return networkid.MessageID(msg), networkid.PartID(part)
	}
	return networkid.MessageID(ext), ""
}
