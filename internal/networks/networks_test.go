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
	"context"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

// Every linked network starts its bridge on a fresh SQLite file (migrations included) and lists login flows.
func TestEveryNetworkStarts(t *testing.T) {
	for _, id := range IDs() {
		t.Run(id, func(t *testing.T) {
			f, ok := Lookup(id)
			if !ok || f.ID != id {
				t.Fatalf("Lookup(%q) = %+v, %v", id, f, ok)
			}
			reg, err := hosts.Start(context.Background(), tempDir(t), []hosts.Factory{f}, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			defer reg.Stop()
			n := reg.Get(id)
			if len(n.Connector.GetLoginFlows()) == 0 {
				t.Fatal("no login flows")
			}
		})
	}
}

// A stopped bridge can still write SQLite side files for a moment. t.TempDir fails the test
// when its cleanup meets such a file, so this cleanup retries.
func tempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "networks-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("could not remove %s", dir)
	})
	return dir
}
