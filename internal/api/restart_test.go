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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

// A send right after a host restart on the same data dir must not reuse a
// message id of the earlier run.
func TestFakeSendAfterRestart(t *testing.T) {
	dir := t.TempDir()
	start := func() (*harness, func()) {
		fake := fakenet.New()
		log := zerolog.Nop()
		reg, err := hosts.Start(context.Background(), dir, []hosts.Factory{
			{ID: fakenet.NetworkID, New: func() (bridgev2.NetworkConnector, error) { return fake, nil }},
		}, log)
		if err != nil {
			t.Fatal(err)
		}
		srv := &Server{Reg: reg, Logins: login.NewEngine(reg, log), Token: token, Version: "test", Log: log,
			Extra: fakenet.Routes(fake, reg.Store)}
		ts := httptest.NewServer(srv.Handler())
		return &harness{t: t, ts: ts, reg: reg, fake: fake}, func() {
			ts.Close()
			reg.Stop()
		}
	}
	h, stop := start()
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	next(t, conn)
	client.Incoming("dm-alice", "alice", "m1", "hello")
	until(t, conn, wire.EventMessageUpsert)
	var first wire.SendResponse
	for _, cm := range []string{"before-1", "before-2", "before-3"} {
		h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": cm, "text": "before"}, &first, 200)
	}
	_ = conn.CloseNow()
	stop()

	h, stop = start()
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	for h.fake.Client("15550001111") == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	var after wire.SendResponse
	h.doJSON("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "after-1", "text": "after restart"}, &after, 200)
	if after.ExternalMessageID == first.ExternalMessageID {
		t.Fatalf("id %s reused", after.ExternalMessageID)
	}
}
