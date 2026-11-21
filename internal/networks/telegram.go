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

package networks

import (
	"fmt"
	"os"
	"strconv"

	"maunium.net/go/mautrix/bridgev2"

	tgconnector "go.mau.fi/mautrix-telegram/pkg/connector"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

func init() { register("telegram", Telegram) }

// Telegram reads the API credentials from TELEGRAM_API_ID and TELEGRAM_API_HASH.
// Upstream requires both (network.api_id / network.api_hash in its config) and
// rejects the placeholder hash of its example config.
func Telegram() hosts.Factory {
	return hosts.Factory{ID: "telegram", Check: requireEnv("TELEGRAM_API_ID", "TELEGRAM_API_HASH"), New: func() (bridgev2.NetworkConnector, error) {
		c := &tgconnector.TelegramConnector{}
		if err := hosts.LoadExampleConfig(c); err != nil {
			return nil, fmt.Errorf("telegram example config: %w", err)
		}
		c.Config.APIID = 0
		c.Config.APIHash = ""
		if raw := os.Getenv("TELEGRAM_API_ID"); raw != "" {
			apiID, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("TELEGRAM_API_ID must be an integer")
			}
			c.Config.APIID = apiID
		}
		c.Config.APIHash = os.Getenv("TELEGRAM_API_HASH")
		if c.Config.DeviceInfo.AppVersion == "auto" {
			c.Config.DeviceInfo.AppVersion = UpstreamVersion
		}
		if c.Config.DeviceInfo.SystemVersion == "auto" {
			c.Config.DeviceInfo.SystemVersion = ""
		}
		if c.Config.DeviceInfo.DeviceModel == "auto" || c.Config.DeviceInfo.DeviceModel == "" {
			c.Config.DeviceInfo.DeviceModel = "Roomcord"
		}
		return c, nil
	}}
}
