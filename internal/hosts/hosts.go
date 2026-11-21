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

// Package hosts runs one bridgev2.Bridge per network, each with its own SQLite
// database and a localmatrix connector instead of a homeserver.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/commands"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/localmatrix"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/store"
)

// bridgeConfigYAML is the bridgev2 core config shared by every network. Notable
// choices: split_portals isolates users from each other, the permission entry lets
// every local user log in, relay and room cleanup are off, the core dedupes repeated
// sends by event id.
const bridgeConfigYAML = `
command_prefix: "!bridge"
personal_filtering_spaces: false
private_chat_portal_meta: true
async_events: false
split_portals: true
bridge_status_notices: none
unknown_error_auto_reconnect: 1m
unknown_error_max_auto_reconnects: 10
tag_only_on_create: true
mute_only_on_create: true
deduplicate_matrix_messages: true
portal_event_buffer: 64
kick_matrix_users: false
cleanup_on_logout:
  enabled: false
relay:
  enabled: false
portal_create_filter:
  mode: deny
permissions:
  "roomcord.local": user
backfill:
  enabled: true
  max_initial_messages: 50
  max_catchup_messages: 500
  unread_hours_threshold: 720
  threads:
    max_initial_messages: 50
  queue:
    enabled: false
`

// Factory builds a network connector with its config already loaded.
type Factory struct {
	ID  string
	New func() (bridgev2.NetworkConnector, error)
	// Check reports missing host configuration (for example unset env variables) before the
	// connector's own ValidateConfig runs. Optional.
	Check func() error
}

type Network struct {
	ID        string
	Bridge    *bridgev2.Bridge
	Matrix    *localmatrix.Connector
	Connector bridgev2.NetworkConnector
	// ConfigError is set when the connector's own config validation failed. The
	// network still starts so its flows can be listed and the failure is visible.
	ConfigError error
}

type Registry struct {
	Store    *store.Store
	MediaDir string
	networks map[string]*Network
	// unavailable holds networks that were asked for but cannot run in this binary, with the reason.
	unavailable map[string]string
}

// AddUnavailable records a requested network that this binary cannot run (for example built without its tag).
func (r *Registry) AddUnavailable(id, reason string) {
	if r.unavailable == nil {
		r.unavailable = make(map[string]string)
	}
	r.unavailable[id] = reason
}

// Unavailable returns the requested networks that cannot run, by id.
func (r *Registry) Unavailable() map[string]string { return r.unavailable }

func (r *Registry) Get(id string) *Network { return r.networks[id] }

func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.networks))
	for id := range r.networks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func DefaultBridgeConfig() (*bridgeconfig.BridgeConfig, error) {
	var cfg bridgeconfig.BridgeConfig
	if err := yaml.Unmarshal([]byte(bridgeConfigYAML), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadExampleConfig fills a connector's config struct from its embedded example
// config, which is how upstream gets its defaults.
func LoadExampleConfig(conn bridgev2.NetworkConnector) error {
	example, data, _ := conn.GetConfig()
	if data == nil || example == "" {
		return nil
	}
	return yaml.Unmarshal([]byte(example), data)
}

func Start(ctx context.Context, dataDir string, factories []Factory, log zerolog.Logger) (*Registry, error) {
	st, err := store.Open(filepath.Join(dataDir, "host.db"))
	if err != nil {
		return nil, err
	}
	reg := &Registry{Store: st, MediaDir: filepath.Join(dataDir, "media"), networks: make(map[string]*Network)}
	for _, f := range factories {
		n, err := startNetwork(ctx, dataDir, reg, f, log.With().Str("network", f.ID).Logger())
		if err != nil {
			reg.Stop()
			return nil, fmt.Errorf("start %s: %w", f.ID, err)
		}
		reg.networks[f.ID] = n
	}
	return reg, nil
}

func startNetwork(ctx context.Context, dataDir string, reg *Registry, f Factory, log zerolog.Logger) (*Network, error) {
	conn, err := f.New()
	if err != nil {
		return nil, err
	}
	n := &Network{ID: f.ID, Connector: conn}
	if f.Check != nil {
		n.ConfigError = f.Check()
	}
	if v, ok := conn.(bridgev2.ConfigValidatingNetwork); ok && n.ConfigError == nil {
		n.ConfigError = v.ValidateConfig()
	}
	if n.ConfigError != nil {
		log.Warn().Err(n.ConfigError).Msg("Network config is invalid, logins will fail")
	}
	db, err := dbutil.NewFromConfig("roomcord-bridge-host/"+f.ID, dbutil.Config{PoolConfig: dbutil.PoolConfig{
		Type:         "sqlite3-fk-wal",
		URI:          fmt.Sprintf("file:%s?_txlock=immediate", filepath.Join(dataDir, f.ID+".db")),
		MaxOpenConns: 5,
		MaxIdleConns: 1,
	}}, dbutil.ZeroLogger(log.With().Str("db_section", "main").Logger()))
	if err != nil {
		return nil, err
	}
	cfg, err := DefaultBridgeConfig()
	if err != nil {
		return nil, err
	}
	n.Matrix = localmatrix.New(f.ID, reg.Store, reg.MediaDir, log.With().Str("component", "localmatrix").Logger())
	n.Bridge = bridgev2.NewBridge("", db, log, cfg, n.Matrix, conn, commands.NewProcessor)
	if err = n.Bridge.Start(ctx); err != nil {
		var dbErr bridgev2.DBUpgradeError
		if errors.As(err, &dbErr) {
			return nil, fmt.Errorf("database upgrade (%s): %w", dbErr.Section, dbErr.Err)
		}
		return nil, err
	}
	return n, nil
}

func (r *Registry) Stop() {
	for _, n := range r.networks {
		if n.Bridge != nil {
			n.Bridge.StopWithTimeout(5 * time.Second)
		}
	}
	if r.Store != nil {
		_ = r.Store.Close()
	}
}
