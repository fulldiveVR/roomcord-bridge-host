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

package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The server runs as its own process, like a real network.
func TestBinaryServes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "fakechat")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	cmd := exec.Command(bin, "-addr", addr, "-control-token", "t", "-seed", "1")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	})
	base := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no health: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	body, _ := json.Marshal(map[string]string{"username": "demo", "password": "demo-pass"})
	resp, err := http.Post(base+"/api/v1/auth/password", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var sess struct {
		AccessToken string `json:"accessToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()
	if resp.StatusCode != 200 || sess.AccessToken == "" {
		t.Fatalf("login %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", base+"/control/media", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var catalog []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&catalog)
	resp.Body.Close()
	if len(catalog) != 13 {
		t.Fatalf("catalog has %d entries", len(catalog))
	}
}
