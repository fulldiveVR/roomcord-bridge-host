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
	"runtime"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

func TestHostOSName(t *testing.T) {
	for goos, want := range map[string]string{"darwin": "Mac OS", "windows": "Windows", "linux": "Linux", "freebsd": "freebsd"} {
		if got := hostOSName(goos); got != want {
			t.Errorf("hostOSName(%q) = %q, want %q", goos, got, want)
		}
	}
}

// The pairing props must name a Chrome companion, or the phone shows "Other device" with a "?" icon.
func TestWhatsAppPairsAsChrome(t *testing.T) {
	for _, tc := range []struct {
		name, osEnv, browserEnv, wantOS string
		wantPlatform                    waCompanionReg.DeviceProps_PlatformType
	}{
		{"default", "", "", hostOSName(runtime.GOOS), waCompanionReg.DeviceProps_CHROME},
		{"override", "Test OS", "firefox", "Test OS", waCompanionReg.DeviceProps_FIREFOX},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WHATSAPP_OS_NAME", tc.osEnv)
			t.Setenv("WHATSAPP_BROWSER_NAME", tc.browserEnv)
			f, _ := Lookup("whatsapp")
			reg, err := hosts.Start(context.Background(), tempDir(t), []hosts.Factory{f}, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			defer reg.Stop()
			if got := store.DeviceProps.GetOs(); got != tc.wantOS {
				t.Errorf("DeviceProps.Os = %q, want %q", got, tc.wantOS)
			}
			if got := store.DeviceProps.GetPlatformType(); got != tc.wantPlatform {
				t.Errorf("DeviceProps.PlatformType = %v, want %v", got, tc.wantPlatform)
			}
		})
	}
}
