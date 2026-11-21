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

//go:build signal

package networks

import (
	"maunium.net/go/mautrix/bridgev2"

	sigconnector "go.mau.fi/mautrix-signal/pkg/connector"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

func init() { register("signal", Signal) }

// Signal links mautrix-signal, which needs the Rust static library libsignal_ffi.a at link time (docs/networks.md).
func Signal() hosts.Factory {
	return exampleFactory("signal", func() bridgev2.NetworkConnector { return &sigconnector.SignalConnector{} }).WithConfig(func(conn bridgev2.NetworkConnector) {
		conn.(*sigconnector.SignalConnector).Config.DeviceName = "Roomcord"
	})
}
