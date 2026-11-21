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
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

// BRIDGE_HOST_FAKE_ID: the fake network runs under another id, so an API that also has an in-process `fake` can register both.
func TestFakeNetworkUnderAnotherID(t *testing.T) {
	const id = "bridge_fake"
	fake := fakenet.NewWithID(id)
	log := zerolog.Nop()
	reg, err := hosts.Start(context.Background(), t.TempDir(), []hosts.Factory{
		{ID: fake.ID(), New: func() (bridgev2.NetworkConnector, error) { return fake, nil }},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, reg: reg, fake: fake}
	srv := &Server{Reg: reg, Logins: login.NewEngine(reg, log), Token: token, Version: "test", Log: log, Extra: fakenet.Routes(fake, reg.Store)}
	h.ts = newTestServer(t, srv, reg)

	var nets []wire.Network
	h.doJSON("GET", "/networks", nil, &nets, 200)
	if len(nets) != 1 || nets[0].ID != id || !nets[0].Available {
		t.Fatalf("networks %+v", nets)
	}
	var r wire.LoginResponse
	h.doJSON("POST", "/logins/start", map[string]any{"network": id, "userId": "u1", "flowId": "phone"}, &r, 200)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": map[string]string{"phone_number": "+15555550123"}}, &r, 200)
	h.doJSON("POST", "/logins/"+r.ProcessID+"/submit", map[string]any{"stepId": r.Step.StepID, "fields": map[string]string{"code": fakenet.MagicCode}}, &r, 200)
	if r.Step.Kind != wire.KindDone || !strings.HasPrefix(r.Step.LoginID, id+":") {
		t.Fatalf("done %+v", r.Step)
	}
	h.doJSON("POST", "/fake/inject", map[string]any{"loginId": r.Step.LoginID, "kind": "message", "conversationId": "dm-alice", "messageId": "m1", "text": "hi"}, nil, 204)
	if code, raw := h.do("POST", "/logins/start", map[string]any{"network": "fake", "userId": "u1", "flowId": "phone"}); code != 404 {
		t.Fatalf("the default id must not answer: %d %s", code, raw)
	}
}

// Flow `emoji`: an emoji confirmation step (like Google Messages), so the emoji path runs without an account.
func TestFakeEmojiFlow(t *testing.T) {
	h := newHarness(t)
	var nets []wire.Network
	h.doJSON("GET", "/networks", nil, &nets, 200)
	if len(nets) != 1 || len(nets[0].Flows) != 2 || nets[0].Flows[0].ID != "phone" || nets[0].Flows[1].ID != "emoji" {
		t.Fatalf("flows %+v", nets)
	}
	var r wire.LoginResponse
	h.doJSON("POST", "/logins/start", map[string]any{"network": "fake", "userId": "u1", "flowId": "emoji"}, &r, 200)
	if r.Step.Kind != wire.KindExternalApprovalWait || r.Step.Emoji != fakenet.ConfirmEmoji || !r.Step.Pollable || r.Step.Instructions == "" ||
		strings.Contains(r.Step.Instructions, fakenet.ConfirmEmoji) {
		t.Fatalf("emoji step %+v", r.Step)
	}
	h.doJSON("DELETE", "/logins/"+r.ProcessID, nil, nil, 204)
}
