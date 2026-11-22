# roomcord-bridge-host

`roomcord-bridge-host` is one Go process that links the `pkg/connector` packages of the mautrix and Beeper bridges and runs each of them on its own `bridgev2.Bridge`. It has no Matrix homeserver and no appservice. The Matrix side of bridgev2 (`MatrixConnector`, `MatrixAPI`) is implemented locally: every call that the bridge core makes towards "Matrix" becomes an event in a per-login outbox.

A client uses the host over HTTP/JSON and WebSocket with a bearer token. The API gives logins, conversations, messages, media and an event stream. The API is defined in [PROTOCOL.md](PROTOCOL.md).

## Status

Experimental.

- Logins on real accounts are not validated. For each network, only the first login step was observed. No real account was linked.
- Everything after the login is tested only with the test networks: the built-in fake network and FakeChat (a separate server, section FakeChat): message conversion, history, backfill, media, reconnects, logout.
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
| `fakechat` | this repository (`internal/fakechat`) | default, test network, needs `FAKECHAT_URL` (section FakeChat) |

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
make build-fakechat # bin/fakechat, the FakeChat test network server
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
| `BRIDGE_HOST_FAKE` | unset | `true` adds the test network `fake`, the control endpoints under `/fake/` and the fake provider pages under `/fake-provider/`, which need no token (PROTOCOL.md sections 10 and 10a). Do not set it in a real deployment. |
| `BRIDGE_HOST_FAKE_ID` | `fake` | Network id of the fake network. |
| `BRIDGE_HOST_FAKE_CAPTURE_URL` | `http://127.0.0.1:<port>/fake-provider/login` | Page that the capture step of the fake flow `web` opens (PROTOCOL.md section 10a). |
| `FAKECHAT_URL` | unset | Base URL of a FakeChat server, for the network `fakechat`. Without it, `fakechat` is listed with `available: false`. |
| `FAKECHAT_WEB_URL` | `FAKECHAT_URL` | Base URL of the FakeChat web login in the capture step, when the client reaches the server under another address. |

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
internal/fakenet/          deterministic bridgev2 network for tests (in process)
internal/fakechat/         FakeChat: server, network API shapes, media files, bridgev2 connector
cmd/fakechat/              main of the FakeChat server
internal/wire/             JSON shapes
docs/networks.md           per network: pinned commit, flows, first login step, config, build
```

## FakeChat (test network)

FakeChat is a fake messaging network that runs as its own process. It has accounts, logins, chats, a message store, media and an event stream, all in memory. The host links a bridgev2 connector for it (network id `fakechat`) that talks to the server only over HTTP and WebSocket. So a test walks the same path as with a real network: desktop app, bridge host, connector, network server. No real account is needed.

The built-in `fake` network (`BRIDGE_HOST_FAKE`) is different: it runs inside the host and is driven through `/fake/*` on the host. FakeChat adds no route to the host. Its control API is on the FakeChat server.

### Release builds

The connector is in the default binary, but it is inert: it starts only when `BRIDGE_HOST_NETWORKS` names `fakechat`, and its logins work only when `FAKECHAT_URL` is set. The desktop app passes a fixed network list and no `FAKECHAT_URL`, so a release build never lists it. Never point `FAKECHAT_URL` at a FakeChat server in a real deployment.

### Run FakeChat and the host

```
make build build-fakechat
bin/fakechat -addr 127.0.0.1:29400 -control-token fakechat-control -seed 1 &

BRIDGE_HOST_TOKEN=dev-token BRIDGE_HOST_NETWORKS=fakechat FAKECHAT_URL=http://127.0.0.1:29400 \
  bin/roomcord-bridge-host
```

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-addr` | `FAKECHAT_ADDR` | `127.0.0.1:29400` | Listen address. |
| `-control-token` | `FAKECHAT_CONTROL_TOKEN` | `fakechat-control` | Bearer token of `/control/*`. |
| `-seed` | `FAKECHAT_SEED` | unset | An integer. Ids, tokens and message timestamps are deterministic (a virtual clock from 2026-01-01, one second per event). Without it, timestamps are real time and tokens are random. Ids are counters. Without a seed, every id also carries the start time of the server state (`msg-<epoch>-000003`), so a restarted server never reuses an id that a host stored before. With a seed, ids repeat on every start (`msg-000003`): give the host a fresh data dir after a FakeChat restart. |
| `-public-url` | `FAKECHAT_PUBLIC_URL` | request host | Base URL in QR instructions. |
| `-qr-ttl` | | `60s` | Lifetime of one QR code. The connector shows a new code 3 times, then the login fails with `expired`. |

The state is in memory. A restart or `POST /control/reset` starts again from the seed. The web pages and the phone page need no token: bind FakeChat to a loopback address.

### Accounts and login flows

| Account | Username, password | Phone | 2FA code |
|---|---|---|---|
| `demo` (Demo User) | `demo`, `demo-pass` | `+15550100001` | none |
| `secure` (Secure User) | `secure`, `secure-pass` | `+15550100002` | `654321` |

A phone login with an unknown number creates the account `p<digits>`.

| Flow | Steps on the wire | Test paths |
|---|---|---|
| `qr` | `qr` (pollable, refreshed with the same `stepId` when the code expires), `done` | Approve on the phone page `GET /phone` (pick the account, press Approve) or with `POST /control/qr/{qrId}/approve`. |
| `phone` | `phone_number_input`, `code_input`, `done` | SMS code is always `123456`. A number that starts with `+999` gives `invalid_phone_number`. A wrong code gives `invalid_code` with the attempts left in the message; the third wrong code fails the login. |
| `password` | `form_input` (username, password), `code_input` when the account has 2FA, `done` | A wrong password gives the same step with `invalid_input`; a wrong 2FA code gives `invalid_code`. |
| `web` | `web_session_capture`, `done` | Opens `<FAKECHAT_WEB_URL>/web/login`. The sign-in form (and the 2FA form for `secure`) sets the httpOnly cookie `fc_session` and the cookie `fc_user`, then redirects to `/web/app`. Capture fields: `session` (required, cookie `fc_session`, pattern `^fcs_[0-9a-f]+$`) and `user` (optional, cookie `fc_user`). `waitForUrlPattern` matches `<origin>/web/app`. A well-formed session that the server does not know fails the login. |

Session end: `POST /control/accounts/{id}/revoke` ends every session of the account. The stream gets `logged_out` and closes with code 4001, the connector reports `BAD_CREDENTIALS`, so the host shows `needs_reauth`. A new login with `reconnectLoginId` continues the stream: events from the dead period are delivered. `POST /control/accounts/{id}/disconnect` only closes the stream: the connector reports a transient disconnect and reconnects (0.5 s, doubling to 10 s).

### Chats and contacts

Contacts: `alice` (Alice Andersen), `bob`, `carol`, `dave`, `erin`, each with a PNG avatar. Every new account gets four chats:

| Chat id | Kind | Members | History |
|---|---|---|---|
| `dm-alice` | dm | alice | 5 messages, one from the account |
| `dm-bob` | dm | bob | 3 messages |
| `grp-project` | group "Project X" | alice, bob, carol | 60 messages (more than the 50 of the initial backfill) |
| `grp-family` | group "Family" | dave, erin | 2 messages |

### Message types and media

`GET /control/media` lists the media files. All are made at startup or on first use, deterministic, none is a binary in the repository (`clip.mp4`, `voice.ogg` and the WebP files are small base64 constants made with ffmpeg and cwebp).

| Name | Kind | Type | Size |
|---|---|---|---|
| `photo.jpg`, `photo.png`, `anim.gif`, `photo.webp` | image | jpeg, png, gif (3 frames), webp | small |
| `clip.mp4` | video | H.264, 96x64, 1 s | 2 KB |
| `voice.ogg` | voice | Opus, 1.5 s | 3 KB |
| `tone.wav` | audio | PCM, 2 s | 32 KB |
| `doc.pdf`, `archive.zip` | file | pdf, zip | small |
| `sticker.webp` | sticker | webp 128x128 | 2 KB |
| `limit-8mb.bin` | file | octet-stream | exactly 8 MiB (8388608 bytes), the relay limit |
| `oversize-9mb.bin` | file | octet-stream | 9 MiB |
| `oversize-photo.bmp` | image | bmp 1800x1600 | 8640054 bytes |

How the connector maps them (as the WhatsApp connector does): text with formatting entities becomes `m.text` with HTML (the host passes the plain text); media is downloaded from FakeChat and stored by the host; voice gets the MSC3245 voice flag; a sticker is an `m.sticker`; a location is `m.location` with a `Location: ...` body; a contact is a `.vcf` file (`text/vcard`); member joined or left and group rename become chat info changes (`participant_upsert`, `conversation_upsert` on the wire); edits, deletes, reactions, read receipts and typing are the bridgev2 events of the same name. Messages from the account itself on another device arrive as `outbound`.

Outbound, the connector sends text, media with a caption, replies, edits, deletes, reactions, read receipts and typing. The host API reaches text, media, replies and read receipts. It has no route for edit, delete, react or typing; the tests send those as Matrix events through bridgev2.

### Control API

Every call needs `Authorization: Bearer <control token>`. Bodies are JSON.

| Call | Body | Effect |
|---|---|---|
| `POST /control/reset` | `{ seed? }` | Drop all state, seed again. Open streams close with 4001. |
| `GET /control/accounts` | | Accounts with credentials, session and stream counts, head seq. |
| `POST /control/accounts` | `{ id?, displayName?, phone?, username?, password?, twoFactorCode? }` | Create an account with the four seed chats. |
| `GET`, `POST /control/accounts/{id}/chats` | `{ id?, kind: dm or group, title?, members: [userId] }` | List chats, or create one (the stream gets a `chat_update`). |
| `POST /control/accounts/{id}/inject` | `{ type?, chatId, from?, text?, entities?, replyTo?, media?, kind?, location?, contact?, messageId?, emoji?, typing?, userId?, title? }` | One remote action. `type`: `message` (default), `edit`, `delete`, `reaction_add`, `reaction_remove`, `read`, `typing`, `member_join`, `member_leave`, `rename`. `from` defaults to the first other member; `from` = the account id is a message from another device. `media` is a catalog name. Returns `{ seq, message? }`. |
| `POST /control/accounts/{id}/scenario` | `{ name, chatId?, count? }` | Runs a scenario (default chat `grp-project`). Returns `{ chatId, steps: [{ name, type, messageId, seq }] }`. |
| `GET /control/accounts/{id}/sent` | | What the network client did: `send`, `upload`, `edit`, `delete`, `react`, `unreact`, `read`, `typing`, with text, media name, type, size, emoji. |
| `POST /control/accounts/{id}/disconnect` | | Close the account's streams (transient disconnect). |
| `POST /control/accounts/{id}/revoke` | | End every session (forced logout). |
| `GET /control/qr`, `POST /control/qr/{qrId}/approve` | `{ accountId? }` (default `demo`) | Pending QR codes; approve one. |
| `POST /control/faults`, `DELETE /control/faults` | `{ kind: rate_limit or server_error, scope?: any, auth, send, history, media, events, count? }` | The next `count` (default 1) calls in the scope answer 429 (`Retry-After: 1`) or 500. A send fails on the host with 502 `network_unavailable`; a stream fault delays the reconnect. |
| `GET /control/media` | | The media catalog. |

Scenarios:

| Name | Steps |
|---|---|
| `all-types` | `text`, `long_text` (about 4400 characters), `formatted` (bold, italic, code, link, strike), `emoji_only`, `reply`, `image_jpg` (with a caption), `image_png`, `image_gif`, `image_webp`, `video`, `voice`, `audio`, `file_pdf`, `file_zip`, `sticker`, `location`, `contact`, `file_at_limit`, `file_oversize`, `image_oversize`, `own_message`, `edit`, `reaction_add`, `reaction_add_2`, `reaction_remove`, `to_delete`, `delete`, `typing_start`, `typing_stop`, `read`, and in a group `member_join`, `member_leave`, `rename` |
| `media` | the ten media steps of `all-types` |
| `oversize` | `file_at_limit`, `file_oversize`, `image_oversize` |
| `group-events` | `member_join`, `member_leave`, `rename` |
| `burst` | `count` text messages (default 20) |

Examples (ids as with `-seed 1`):

```
C='Authorization: Bearer fakechat-control'
curl -H "$C" -d '{"name":"all-types"}' http://127.0.0.1:29400/control/accounts/demo/scenario
curl -H "$C" -d '{"chatId":"dm-alice","text":"hi","replyTo":"msg-000003"}' http://127.0.0.1:29400/control/accounts/demo/inject
curl -H "$C" -d '{"chatId":"dm-bob","media":"oversize-9mb.bin","text":"too big"}' http://127.0.0.1:29400/control/accounts/demo/inject
curl -H "$C" -d '{"type":"reaction_add","chatId":"dm-alice","messageId":"msg-000005","emoji":"👍"}' http://127.0.0.1:29400/control/accounts/demo/inject
curl -H "$C" http://127.0.0.1:29400/control/accounts/demo/sent
curl -H "$C" -X POST http://127.0.0.1:29400/control/accounts/demo/revoke
curl -H "$C" -d '{"kind":"rate_limit","scope":"send","count":2}' http://127.0.0.1:29400/control/faults
curl -H "$C" http://127.0.0.1:29400/control/qr
curl -H "$C" -X POST http://127.0.0.1:29400/control/qr/qr-000150/approve -d '{"accountId":"secure"}'
```

### Network API (what the connector uses)

`/api/v1` with `Authorization: Bearer <access token>`. Auth: `POST auth/qr`, `GET auth/qr/{qrId}?wait=<s>` (long poll), `POST auth/phone`, `POST auth/phone/verify`, `POST auth/password`, `POST auth/2fa`, `POST auth/web` (cookie value to access token). Data: `GET me`, `POST logout`, `GET users/{id}`, `GET chats`, `GET chats/{id}`, `GET chats/{id}/messages?before|after=<msgId>&limit=` (oldest first in a page), `POST chats/{id}/messages` (idempotent on `clientId`), `PATCH`/`DELETE chats/{id}/messages/{msgId}`, `PUT`/`DELETE chats/{id}/messages/{msgId}/reactions/{emoji}`, `POST chats/{id}/read`, `POST chats/{id}/typing`, `POST media` (multipart `file`, up to 64 MiB), `GET media/{id}`, `WS events?since=<seq>`. The stream starts with `hello` (the head seq), replays every later event of the account, and skips events that the same session caused. JSON shapes: `internal/fakechat/proto`.

## Security

- The host binds to `127.0.0.1` by default. Do not expose it to a public network. If a client runs on another machine, put the host behind a private network or an authenticated TLS proxy.
- Every request needs the bearer token, also the WebSocket upgrade. The host has one token and no per-user access control. Any holder of the token can use every linked account. The `userId` in the API separates data, it does not authenticate.
- The data directory holds the account sessions of every linked account, the bridged messages and the media files. They are not encrypted by the host. Protect the directory like a credential store and do not commit it.
- The host has no TLS of its own.
- `fakechat` with `FAKECHAT_URL` connects to a test server whose web pages and phone page need no token. Use it only for tests.
- `BRIDGE_HOST_FAKE=true` adds test endpoints that inject events, and fake provider pages under `/fake-provider/` that need no token and answer loopback clients only. Use it only for tests.
- A browser-session login sends session cookies and tokens of the user to the host. The host does not write them to its log (PROTOCOL.md section 3b). Upstream connectors log on their own; check their output before you raise the log level in production.

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
