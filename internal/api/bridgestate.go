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
	"reflect"
	"sync"
	"unsafe"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/status"
)

var queueLockField = sync.OnceValues(func() (int, bool) {
	f, ok := reflect.TypeFor[bridgev2.BridgeStateQueue]().FieldByName("writeLock")
	if !ok || f.Type != reflect.TypeFor[sync.Mutex]() || len(f.Index) != 1 {
		return 0, false
	}
	return f.Index[0], true
})

// queuedState returns the newest state that the connector queued. Upstream
// BridgeStateQueue.Send writes it under the queue's private writeLock, but
// GetPrevUnsent reads it without a lock, so a connector that sends a state from
// its own goroutine (every real one does) races with a concurrent health read.
// The read here takes that lock. When the pinned bridgev2 renames the field, the
// read falls back to the unlocked getter.
func queuedState(bsq *bridgev2.BridgeStateQueue) status.BridgeState {
	if bsq == nil {
		return status.BridgeState{}
	}
	if idx, ok := queueLockField(); ok {
		mu := (*sync.Mutex)(unsafe.Pointer(reflect.ValueOf(bsq).Elem().Field(idx).UnsafeAddr()))
		mu.Lock()
		defer mu.Unlock()
	}
	return bsq.GetPrevUnsent()
}
