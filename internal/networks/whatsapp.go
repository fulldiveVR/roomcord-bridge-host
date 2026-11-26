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
	"runtime"

	"maunium.net/go/mautrix/bridgev2"

	waconnector "go.mau.fi/mautrix-whatsapp/pkg/connector"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

func init() { register("whatsapp", WhatsApp) }

// WhatsApp links as a Chrome companion named after the host OS, so the phone's Linked devices list
// shows it like a WhatsApp Web session. Upstream defaults ("unknown") show "Other device" with a "?" icon.
// The phone reads these props only at pairing: a linked session keeps its old name until it re-links.
func WhatsApp() hosts.Factory {
	return exampleFactory("whatsapp", func() bridgev2.NetworkConnector { return &waconnector.WhatsAppConnector{} }).WithConfig(func(conn bridgev2.NetworkConnector) {
		c := conn.(*waconnector.WhatsAppConnector)
		c.Config.OSName = envOr("WHATSAPP_OS_NAME", hostOSName(runtime.GOOS))
		c.Config.BrowserName = envOr("WHATSAPP_BROWSER_NAME", "chrome")
	})
}

func hostOSName(goos string) string {
	switch goos {
	case "darwin":
		return "Mac OS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return goos
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
