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

// Command fakechat runs the FakeChat test network: a separate server with its own
// accounts, logins, chats, messages and media. The bridge host reaches it through
// the fakechat connector (BRIDGE_HOST_NETWORKS=fakechat, FAKECHAT_URL). See
// README.md, section FakeChat.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", env("FAKECHAT_ADDR", "127.0.0.1:29400"), "listen address (FAKECHAT_ADDR)")
	control := flag.String("control-token", env("FAKECHAT_CONTROL_TOKEN", "fakechat-control"), "bearer token of /control/* (FAKECHAT_CONTROL_TOKEN)")
	seedFlag := flag.String("seed", os.Getenv("FAKECHAT_SEED"), "integer; makes ids, tokens and timestamps deterministic (FAKECHAT_SEED)")
	public := flag.String("public-url", os.Getenv("FAKECHAT_PUBLIC_URL"), "base URL in QR data and page links (FAKECHAT_PUBLIC_URL); default: the request host")
	qrTTL := flag.Duration("qr-ttl", 60*time.Second, "lifetime of one QR code")
	flag.Parse()

	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.DateTime}).With().Timestamp().Str("service", "fakechat").Logger()
	cfg := server.Config{ControlToken: *control, PublicURL: *public, QRTTL: *qrTTL, Log: log}
	if *seedFlag != "" {
		seed, err := strconv.ParseInt(*seedFlag, 10, 64)
		if err != nil {
			log.Fatal().Msg("seed must be an integer")
		}
		cfg.Seed, cfg.Deterministic = seed, true
	}
	srv := server.New(cfg)
	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info().Str("addr", *addr).Bool("deterministic", cfg.Deterministic).Msg("FakeChat listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
