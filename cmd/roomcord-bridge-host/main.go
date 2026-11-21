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
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/api"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/login"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/networks"
)

var Version = "0.1.0"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	level, err := zerolog.ParseLevel(env("BRIDGE_HOST_LOG_LEVEL", "info"))
	if err != nil {
		level = zerolog.InfoLevel
	}
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.DateTime}).Level(level).With().Timestamp().Logger()
	zerolog.DefaultContextLogger = &log

	token := os.Getenv("BRIDGE_HOST_TOKEN")
	if token == "" {
		log.Fatal().Msg("BRIDGE_HOST_TOKEN is required")
	}
	addr := env("BRIDGE_HOST_ADDR", "127.0.0.1:29391")
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		log.Warn().Str("addr", addr).Msg("Listening on a non-loopback address")
	}
	dataDir := env("BRIDGE_HOST_DATA_DIR", "./data")
	if err = os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal().Err(err).Msg("Failed to create data dir")
	}

	var factories []hosts.Factory
	notBuilt := map[string]string{}
	for _, n := range strings.Split(env("BRIDGE_HOST_NETWORKS", "whatsapp,telegram"), ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if f, ok := networks.Lookup(n); ok {
			factories = append(factories, f)
		} else if reason := networks.NotBuilt(n); reason != "" {
			log.Error().Str("network", n).Str("reason", reason).Msg("Network is not built into this binary")
			notBuilt[n] = reason
		} else {
			log.Fatal().Str("network", n).Strs("known", networks.IDs()).Msg("Unknown network in BRIDGE_HOST_NETWORKS")
		}
	}
	var fake *fakenet.Connector
	if os.Getenv("BRIDGE_HOST_FAKE") == "true" {
		fake = fakenet.NewWithID(env("BRIDGE_HOST_FAKE_ID", fakenet.NetworkID))
		factories = append(factories, hosts.Factory{ID: fake.ID(), New: func() (bridgev2.NetworkConnector, error) { return fake, nil }})
	}

	ctx := log.WithContext(context.Background())
	reg, err := hosts.Start(ctx, dataDir, factories, log)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to start networks")
	}
	for id, reason := range notBuilt {
		reg.AddUnavailable(id, reason)
	}
	extra := map[string]http.Handler{}
	if fake != nil {
		extra = fakenet.Routes(fake, reg.Store)
	}
	srv := &api.Server{
		Reg:     reg,
		Logins:  login.NewEngine(reg, log.With().Str("component", "login").Logger()),
		Token:   token,
		Version: Version,
		Log:     log,
		Extra:   extra,
	}
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info().Str("addr", addr).Strs("networks", reg.IDs()).Msg("Bridge host listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("Shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	reg.Stop()
}
