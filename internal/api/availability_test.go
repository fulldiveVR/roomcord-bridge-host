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

package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

func TestNetworkAvailability(t *testing.T) {
	log := zerolog.Nop()
	reg, err := hosts.Start(context.Background(), t.TempDir(), []hosts.Factory{{
		ID:    fakenet.NetworkID,
		New:   func() (bridgev2.NetworkConnector, error) { return fakenet.New(), nil },
		Check: func() error { return errors.New("missing configuration: FAKE_API_KEY") },
	}}, log)
	if err != nil {
		t.Fatal(err)
	}
	reg.AddUnavailable("signal", "built without the signal build tag")
	h := &harness{t: t, reg: reg}
	srv := &Server{Reg: reg, Logins: login.NewEngine(reg, log), Token: token, Version: "test", Log: log}
	h.ts = newTestServer(t, srv, reg)

	var nets []wire.Network
	h.doJSON("GET", "/networks", nil, &nets, 200)
	if len(nets) != 2 {
		t.Fatalf("networks %+v", nets)
	}
	fake, sig := nets[0], nets[1]
	if fake.ID != "fake" || fake.Available || fake.Reason != "missing configuration: FAKE_API_KEY" || len(fake.Flows) != 3 ||
		fake.Capabilities["loginReady"] != false || fake.Capabilities["configError"] != fake.Reason {
		t.Fatalf("fake %+v", fake)
	}
	if sig.ID != "signal" || sig.Available || sig.Reason != "built without the signal build tag" || len(sig.Flows) != 0 {
		t.Fatalf("signal %+v", sig)
	}

	code, raw := h.do("POST", "/logins/start", map[string]any{"network": "fake", "userId": "u1", "flowId": "phone"})
	if code != 503 || !contains(raw, "network_unavailable") || !contains(raw, "FAKE_API_KEY") {
		t.Fatalf("fake start: %d %s", code, raw)
	}
	code, raw = h.do("POST", "/logins/start", map[string]any{"network": "signal", "userId": "u1", "flowId": "qr"})
	if code != 503 || !contains(raw, "network_unavailable") {
		t.Fatalf("signal start: %d %s", code, raw)
	}
	code, _ = h.do("POST", "/logins/start", map[string]any{"network": "nope", "userId": "u1", "flowId": "qr"})
	if code != 404 {
		t.Fatalf("unknown network: %d", code)
	}
}

func newTestServer(t *testing.T, srv *Server, reg *hosts.Registry) *httptest.Server {
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		reg.Stop()
	})
	return ts
}

func contains(raw []byte, s string) bool { return strings.Contains(string(raw), s) }
