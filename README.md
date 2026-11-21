# roomcord-bridge-host

`roomcord-bridge-host` is one Go process that links the `pkg/connector` packages of the mautrix and Beeper bridges and runs each of them on its own `bridgev2.Bridge`. It has no Matrix homeserver and no appservice. The Matrix side of bridgev2 (`MatrixConnector`, `MatrixAPI`) is implemented locally: every call that the bridge core makes towards "Matrix" becomes an event in a per-login outbox.

A client uses the host over HTTP/JSON and WebSocket with a bearer token. The API gives logins, conversations, messages, media and an event stream. The API is defined in [PROTOCOL.md](PROTOCOL.md).

## Status

Experimental.

- Logins on real accounts are not validated. For each network, only the first login step was observed. No real account was linked.
- Everything after the login is tested only with the built-in fake network: message conversion, history, media, reconnects, logout.
- The API can change without notice.

Known limits:

- `history` serves the messages that the host has bridged. It does not fetch older pages from the network on demand.
- Media is one upload per message. Avatars are not exposed.
- Not translated to events: typing, room avatar and topic changes, member leave, chat delete, mark-unread, polls, formatted text (plain text only), disappearing-message timers.
- Room state (members, power levels) is kept in memory. The bridge core rebuilds it after a restart.
- Bridge commands (`!wa ...`, `!tg ...`) are not reachable. There is no management room.
- Login steps of type `webauthn` and `client_http` are not supported. They end with `failed` and `unsupported_method`.

## Supported networks

| Network id | Upstream module | Build |
|---|---|---|
| `whatsapp` | `go.mau.fi/mautrix-whatsapp` | default |
| `telegram` | `go.mau.fi/mautrix-telegram` | default, needs `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` |
| `messenger` | `go.mau.fi/mautrix-meta` | default |
| `instagram` | `go.mau.fi/mautrix-meta` | default |
| `slack` | `go.mau.fi/mautrix-slack` | default |
| `linkedin` | `go.mau.fi/mautrix-linkedin` | default |
| `twitter` (X) | `go.mau.fi/mautrix-twitter` | default |
| `gmessages` | `go.mau.fi/mautrix-gmessages` | default |
| `line` | `github.com/beeper/line` | default |
| `groupme` | `github.com/beeper/groupme` | default |
| `tumblr` | `github.com/beeper/tumblr-dms` | default |
| `signal` | `go.mau.fi/mautrix-signal` | build tag `signal`, needs `libsignal_ffi.a` |

Login flows, first login steps, configuration and build notes for each network are in [docs/networks.md](docs/networks.md).

A network that needs a build tag which the binary lacks is listed by `GET /networks` with `available: false` and a reason.

## Build

Requirements, as verified on macOS arm64:

- Go 1.27.4 or newer.
- cgo with a C compiler. `mattn/go-sqlite3` and `go.mau.fi/webp` need it. `CGO_ENABLED=0` does not build.
- No libolm. The host does not import the Matrix crypto packages.

```
make build          # bin/roomcord-bridge-host, about 146 MB
make build-signal LIBSIGNAL_DIR=<directory that holds libsignal_ffi.a>
make vet
make test           # go test -race ./...
```

The Signal build needs the Rust static library `libsignal_ffi.a`. The steps are in [docs/networks.md](docs/networks.md), section `signal`.

### Pinned versions

Every bridge is pinned to an exact commit in `go.mod`. All bridges compile against one `maunium.net/go/mautrix` version.

| Module | Version |
|---|---|
| `go.mau.fi/mautrix-whatsapp` | `v0.2611.1-0.20261120223127-eecc2538bf7e` |
| `go.mau.fi/mautrix-telegram` | `v0.2611.1-0.20261120192921-8c20292b3b29` |
| `go.mau.fi/mautrix-meta` | `v0.2611.1-0.20261120174120-721148544bfb` |
| `go.mau.fi/mautrix-signal` | `v0.2611.1-0.20261118112413-11ede28800d5` (tag `signal`) |
| `go.mau.fi/mautrix-slack` | `v0.2611.1-0.20261120192952-6553d937cc8d` |
| `go.mau.fi/mautrix-linkedin` | `v0.2611.1-0.20261120174201-6a016fbdf6e3` |
| `go.mau.fi/mautrix-twitter` | `v0.2609.1-0.20261120174138-77b2290c8537` |
| `go.mau.fi/mautrix-gmessages` | `v0.2611.1-0.20261119194137-ed5dc19eb2b4` |
| `github.com/beeper/line` | `v0.0.0-20261120193203-6b8d57b4e113` |
| `github.com/beeper/groupme` | `v0.0.0-20261120193234-1bbba535abb9` |
| `github.com/beeper/tumblr-dms` | `v0.1.1-0.20261120193536-78a2515b6853` |
| `maunium.net/go/mautrix` | `v0.33.1-0.20261120111454-33abe66d85a0` |
| `go.mau.fi/whatsmeow` | `v0.0.0-20261116172437-19d61352436f` |

`go.mod` has two `replace` directives. They are copied from the bridges' own `go.mod` files, because Go ignores `replace` in dependencies:

- `github.com/imroc/req/v3 => github.com/beeper/req/v3` (mautrix-meta, mautrix-twitter)
- `github.com/slack-go/slack => github.com/beeper/slackgo` (mautrix-slack)

Be careful with `go mod tidy` and `go get`. They can move a commit pin. To add a bridge, run `go get <module>/pkg/connector@<full commit hash>`. Then check `git diff go.mod`: no existing line may change.

## Run

```
export BRIDGE_HOST_TOKEN=<a long random string>
make run
```

Check that the host runs:

```
curl -H "Authorization: Bearer $BRIDGE_HOST_TOKEN" http://127.0.0.1:29391/health
```

### Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `BRIDGE_HOST_TOKEN` | required | Bearer token for every request. The host does not start without it. |
| `BRIDGE_HOST_ADDR` | `127.0.0.1:29391` | Listen address. The host logs a warning for a non-loopback address. |
| `BRIDGE_HOST_DATA_DIR` | `./data` | Holds `host.db`, one `<network>.db` per network, and `media/`. |
| `BRIDGE_HOST_NETWORKS` | `whatsapp,telegram` | Comma list of the network ids to start. An unknown id stops the host. |
| `BRIDGE_HOST_LOG_LEVEL` | `info` | zerolog level. |
| `TELEGRAM_API_ID`, `TELEGRAM_API_HASH` | unset | Telegram app credentials from my.telegram.org. Without them, Telegram is listed with `available: false` and every Telegram login returns `503 network_unavailable`. |
| `BRIDGE_HOST_FAKE` | unset | `true` adds the test network `fake` and the control endpoints under `/fake/` (PROTOCOL.md section 10). Do not set it in a real deployment. |
| `BRIDGE_HOST_FAKE_ID` | `fake` | Network id of the fake network. |

No other network needs an environment variable. Each network uses the defaults of its upstream example config, plus a shared bridgev2 core config in `internal/hosts` (split portals, relay off, backfill on with 50 initial messages).

## Layout

```
cmd/roomcord-bridge-host/  main: environment config, network list, HTTP server
internal/localmatrix/      MatrixConnector and MatrixAPI, Matrix content to wire events, local media store
internal/hosts/            one bridgev2.Bridge and one SQLite file per network, shared core config
internal/networks/         one file per network factory (the only bridge imports)
internal/login/            login step manager, bridgev2 step to wire step mapping
internal/store/            host.db: outbox with a per-login sequence, conversation and message views, media index
internal/api/              HTTP and WebSocket handlers, end-to-end tests over the fake network
internal/fakenet/          deterministic bridgev2 network for tests
internal/wire/             JSON shapes
docs/networks.md           per network: pinned commit, flows, first login step, config, build
```

## Security

- The host binds to `127.0.0.1` by default. Do not expose it to a public network. If a client runs on another machine, put the host behind a private network or an authenticated TLS proxy.
- Every request needs the bearer token, also the WebSocket upgrade. The host has one token and no per-user access control. Any holder of the token can use every linked account. The `userId` in the API separates data, it does not authenticate.
- The data directory holds the account sessions of every linked account, the bridged messages and the media files. They are not encrypted by the host. Protect the directory like a credential store and do not commit it.
- The host has no TLS of its own.
- `BRIDGE_HOST_FAKE=true` adds test endpoints that inject events. Use it only for tests.

## Disclaimer

The linked bridges are unofficial clients of the messaging networks. A network provider can restrict, lock or ban an account that uses an unofficial client. You use this software at your own risk and you are responsible for compliance with the terms of each network.

This project is not affiliated with, endorsed by or supported by Beeper, Matrix.org, the mautrix developers, or any messaging provider (WhatsApp, Meta, Telegram, Signal, Slack, LinkedIn, X, Google, LINE, GroupMe, Tumblr). All product names are trademarks of their owners.

## Licence

The code of this project is free software under the GNU Affero General Public License, version 3 only (`AGPL-3.0-only`). No later version applies. The full text is in [LICENSE](LICENSE).

The binary links AGPL-3.0 code (the bridges) and GPL-3.0 code (`go.mau.fi/libsignal`, `github.com/beeper/groupme-lib`). Each linked upstream module keeps its own licence. Many of them are "AGPL-3.0 or later". The combined binary is distributed under AGPL-3.0. The licence of every linked module is listed in [NOTICE.md](NOTICE.md).

### Source of a running instance

AGPL-3.0 section 13 gives every user who interacts with this program over a network the right to get its source code. The source is at:

https://github.com/fulldiveVR/roomcord-bridge-host

If you run a modified version for other users, you must offer them the source of your modified version.
