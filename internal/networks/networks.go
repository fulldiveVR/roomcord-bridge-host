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

// Package networks wires the upstream bridge connectors (mostly AGPL-3.0) into the host.
// Each network file registers one factory; optional networks sit behind build tags.
package networks

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
)

// UpstreamVersion is the calendar version of the pinned bridge releases.
const UpstreamVersion = "26.11"

var (
	factories = map[string]func() hosts.Factory{}
	// notBuilt names networks whose code needs a build tag that this binary lacks, with the reason.
	notBuilt = map[string]string{}
)

func register(id string, f func() hosts.Factory) { factories[id] = f }

// Lookup returns the factory of a network linked into this binary.
func Lookup(id string) (hosts.Factory, bool) {
	f, ok := factories[id]
	if !ok {
		return hosts.Factory{}, false
	}
	return f(), true
}

// NotBuilt explains why a known network is missing from this binary ("" when it is not a known optional network).
func NotBuilt(id string) string { return notBuilt[id] }

// IDs lists the networks linked into this binary.
func IDs() []string {
	ids := make([]string, 0, len(factories))
	for id := range factories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// exampleFactory builds a connector with the defaults of its embedded example config.
func exampleFactory(id string, newConn func() bridgev2.NetworkConnector) factory {
	return factory{Factory: hosts.Factory{ID: id, New: func() (bridgev2.NetworkConnector, error) {
		c := newConn()
		if err := hosts.LoadExampleConfig(c); err != nil {
			return nil, fmt.Errorf("%s example config: %w", id, err)
		}
		return c, nil
	}}}
}

type factory struct{ hosts.Factory }

// WithConfig changes the loaded example config before the connector starts.
func (f factory) WithConfig(change func(bridgev2.NetworkConnector)) hosts.Factory {
	next := f.New
	f.New = func() (bridgev2.NetworkConnector, error) {
		c, err := next()
		if err == nil {
			change(c)
		}
		return c, err
	}
	return f.Factory
}

// WithCheck adds a host configuration check (see hosts.Factory.Check).
func (f factory) WithCheck(check func() error) factory {
	f.Check = check
	return f
}

// requireEnv returns a Check that names every unset variable.
func requireEnv(keys ...string) func() error {
	return func() error {
		var missing []string
		for _, k := range keys {
			if os.Getenv(k) == "" {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("missing configuration: %s", strings.Join(missing, ", "))
		}
		return nil
	}
}
