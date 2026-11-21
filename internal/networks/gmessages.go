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
	"maunium.net/go/mautrix/bridgev2"

	gmconnector "go.mau.fi/mautrix-gmessages/pkg/connector"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

func init() { register("gmessages", GoogleMessages) }

// GoogleMessages is mautrix-gmessages: SMS/RCS of the user's Android phone through Google Messages for web.
func GoogleMessages() hosts.Factory {
	return exampleFactory("gmessages", func() bridgev2.NetworkConnector { return &gmconnector.GMConnector{} }).Factory
}
