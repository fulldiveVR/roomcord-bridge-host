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

// Package connector is the bridgev2 network connector of FakeChat, a test network
// that runs as a separate server (cmd/fakechat). It talks to that server only over
// HTTP and WebSocket, like the connectors of real networks do: four login flows,
// an event stream, backfill, media download and upload, and the account state.
package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	up "go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

const NetworkID = "fakechat"

type Config struct {
	// URL is the base URL of the FakeChat server (FAKECHAT_URL).
	URL string
	// WebURL is the base URL of the web login page in the capture step
	// (FAKECHAT_WEB_URL). Empty: URL.
	WebURL string
}

type Connector struct {
	Bridge *bridgev2.Bridge
	Config Config

	mu      sync.Mutex
	clients map[networkid.UserLoginID]*Client
	// eventMu serializes remote events of all logins (Client.queue).
	eventMu sync.Mutex
}

var (
	_ bridgev2.NetworkConnector        = (*Connector)(nil)
	_ bridgev2.ConfigValidatingNetwork = (*Connector)(nil)
)

func New(cfg Config) *Connector {
	return &Connector{Config: cfg, clients: map[networkid.UserLoginID]*Client{}}
}

func (fc *Connector) Init(br *bridgev2.Bridge)        { fc.Bridge = br }
func (fc *Connector) Start(ctx context.Context) error { return nil }

func checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an http or https URL", name)
	}
	return nil
}

func (fc *Connector) ValidateConfig() error {
	if fc.Config.URL == "" {
		return errors.New("missing configuration: FAKECHAT_URL")
	}
	if err := checkURL("FAKECHAT_URL", fc.Config.URL); err != nil {
		return err
	}
	if fc.Config.WebURL != "" {
		return checkURL("FAKECHAT_WEB_URL", fc.Config.WebURL)
	}
	return nil
}

func (fc *Connector) webURL() string {
	if fc.Config.WebURL != "" {
		return strings.TrimRight(fc.Config.WebURL, "/")
	}
	return strings.TrimRight(fc.Config.URL, "/")
}

func (fc *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{DisplayName: "FakeChat", NetworkURL: fc.Config.URL, NetworkID: NetworkID, BeeperBridgeType: NetworkID, DefaultCommandPrefix: "!fc"}
}

// UserLoginMetadata is stored with each login.
type UserLoginMetadata struct {
	AccessToken string `json:"access_token"`
	// LastSeq is the last event of the stream that was handled.
	LastSeq int64 `json:"last_seq"`
}

// CopyFrom keeps the stream position when the same account logs in again, so the
// events that arrived while the session was dead are delivered.
func (m *UserLoginMetadata) CopyFrom(other any) {
	o, ok := other.(*UserLoginMetadata)
	if !ok || o == nil {
		return
	}
	m.AccessToken = o.AccessToken
	if o.LastSeq > m.LastSeq {
		m.LastSeq = o.LastSeq
	}
}

var _ database.MetaMerger = (*UserLoginMetadata)(nil)

func (fc *Connector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{UserLogin: func() any { return &UserLoginMetadata{} }}
}

func (fc *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

func (fc *Connector) GetConfig() (string, any, up.Upgrader) { return "", nil, nil }

func (fc *Connector) GetBridgeInfoVersion() (int, int) { return 1, 1 }

func (fc *Connector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta, _ := login.Metadata.(*UserLoginMetadata)
	token := ""
	if meta != nil {
		token = meta.AccessToken
	}
	c := newClient(fc, login, token)
	login.Client = c
	fc.mu.Lock()
	fc.clients[login.ID] = c
	fc.mu.Unlock()
	return nil
}

func (fc *Connector) dropClient(c *Client) {
	fc.mu.Lock()
	if fc.clients[c.login.ID] == c {
		delete(fc.clients, c.login.ID)
	}
	fc.mu.Unlock()
}

// Client returns the network client of a login, or nil.
func (fc *Connector) Client(id networkid.UserLoginID) *Client {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.clients[id]
}

func roomFeatures() *event.RoomFeatures {
	file := func(mimes ...string) *event.FileFeatures {
		m := map[string]event.CapabilitySupportLevel{}
		for _, mt := range mimes {
			m[mt] = event.CapLevelFullySupported
		}
		return &event.FileFeatures{MimeTypes: m, Caption: event.CapLevelFullySupported, MaxSize: 64 << 20}
	}
	return &event.RoomFeatures{
		ID: "fakechat.v1",
		Formatting: event.FormattingFeatureMap{
			event.FmtBold: event.CapLevelFullySupported, event.FmtItalic: event.CapLevelFullySupported,
			event.FmtStrikethrough: event.CapLevelFullySupported, event.FmtInlineCode: event.CapLevelFullySupported,
			event.FmtCodeBlock: event.CapLevelFullySupported, event.FmtInlineLink: event.CapLevelFullySupported,
		},
		File: event.FileFeatureMap{
			event.MsgImage:      file("image/*"),
			event.MsgVideo:      file("video/*"),
			event.MsgAudio:      file("audio/*"),
			event.MsgFile:       file("*/*"),
			event.CapMsgVoice:   file("audio/*"),
			event.CapMsgSticker: file("image/*"),
		},
		LocationMessage:     event.CapLevelFullySupported,
		MaxTextLength:       65536,
		Reply:               event.CapLevelFullySupported,
		Edit:                event.CapLevelFullySupported,
		Delete:              event.CapLevelFullySupported,
		Reaction:            event.CapLevelFullySupported,
		ReadReceipts:        true,
		TypingNotifications: true,
	}
}
