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

package networks

import (
	"os"

	"maunium.net/go/mautrix/bridgev2"

	fcconnector "github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/connector"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

func init() { register(fcconnector.NetworkID, FakeChat) }

// FakeChat is the test network (cmd/fakechat). It starts only when
// BRIDGE_HOST_NETWORKS names it, and it has no flows that work until FAKECHAT_URL
// points at a FakeChat server: without it the network is listed with
// available: false. It adds no route to the host API.
func FakeChat() hosts.Factory {
	return hosts.Factory{ID: fcconnector.NetworkID, Check: requireEnv("FAKECHAT_URL"), New: func() (bridgev2.NetworkConnector, error) {
		return fcconnector.New(fcconnector.Config{URL: os.Getenv("FAKECHAT_URL"), WebURL: os.Getenv("FAKECHAT_WEB_URL")}), nil
	}}
}
