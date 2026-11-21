# Networks

One section per network that the host links. Every "first step" below is the exact `step` that `POST /logins/start` returned on macOS arm64 with an empty data dir, followed at once by `DELETE /logins/{id}`. No personal data was typed, no login was completed, no account was linked. Long payloads are shortened.

Common to every network: enable it with `BRIDGE_HOST_NETWORKS=<id>` (comma list, default `whatsapp,telegram`). Each network has its own SQLite file `<data dir>/<id>.db` and its own logins. `go test ./internal/networks` starts every linked network on a fresh database.

Summary (first step kind per flow):

| Network id | Linked | Flows: first step |
|---|---|---|
| `whatsapp` | yes | `qr`: qr, `phone`: phone_number_input |
| `telegram` | yes, needs `TELEGRAM_API_ID`/`TELEGRAM_API_HASH` | `phone`, `qr`, `bot`, `manual`: 503 without credentials |
| `messenger` | yes | `facebook`, `messenger`: web_session_capture; `messenger-lite`, `messenger-lite-android`: form_input |
| `instagram` | yes | `instagram`: web_session_capture; `instagram-password`, `android`: form_input |
| `signal` | tag `signal` | `qr`: qr |
| `slack` | yes | `email`: form_input; `token`: web_session_capture; `app`: form_input |
| `linkedin` | yes | `cookies`: web_session_capture; `password`: form_input |
| `twitter` (X) | yes | `cookies`: web_session_capture; `password`: form_input |
| `gmessages` | yes | `google`: web_session_capture |
| `line` | yes | `dev.highest.matrix.line.email_login`: form_input |
| `groupme` | yes | `web`: web_session_capture; `password`, `access-token`: form_input |
| `tumblr` | yes | `browser`: web_session_capture; `password`: form_input |

Mapping gaps (`failed` with `unsupported_method`): bridgev2 `webauthn` (mautrix-whatsapp passkey accounts), `client_http` (no bridge sends it unless the client opts in; the host does not), unknown display types, and the `can_cancel` back action (no route). A client without a browser or web view cannot complete a `web_session_capture` step, except with a paste of the values (PROTOCOL.md section 3b).

## Capture flows and their sources

Read from the source of the pinned modules, not run. No account was logged in. "Unverified" marks what the source does not say.

`jsRequired` follows PROTOCOL.md section 3b: `true` only when upstream sets `js_required` (no pinned connector does), or when a required field has only `special` sources.

Flows that start with a capture step:

| Network, flow id | Step id, URL | Fields and sources | `waitForUrlPattern`, `extractJs` | `jsRequired` | httpOnly cookies (unverified, from general knowledge) |
|---|---|---|---|---|---|
| `messenger` `facebook` | `fi.mau.meta.cookies`, `https://www.facebook.com/`, user agent set | `xs`, `c_user`, `datr`: required, `cookie`, domain `facebook.com` | pattern set, no JS | absent | `xs`, `datr` |
| `messenger` `messenger` | `fi.mau.meta.cookies`, `https://www.messenger.com/?no_redirect=true`, user agent set | the same three, domain `messenger.com` | pattern set, no JS | absent | `xs`, `datr` |
| `instagram` `instagram` | `fi.mau.meta.cookies`, `https://www.instagram.com/accounts/login/` | required `sessionid`, `csrftoken`, `ds_user_id`; optional `rur`, `shbid`, `shbts`, `mid`, `ig_did`. All `cookie`, domain `instagram.com` | pattern set, no JS | absent | `sessionid` |
| `twitter` `cookies` | `fi.mau.twitter.login.enter_cookies`, `https://x.com/i/flow/login` | `ct0`, `auth_token`: required, `cookie`, no domain | none, no JS | absent | `auth_token` |
| `gmessages` `google` | `fi.mau.gmessages.google_account`, `https://accounts.google.com/AccountChooser?continue=https://messages.google.com/web/config` | required `SID`, `HSID`, `SSID`, `APISID`, `SAPISID` (`cookie`, domain `.google.com`), `OSID` (`cookie`, domain `messages.google.com`); optional `__Secure-1PSIDTS` | none, no JS | absent | `HSID`, `SSID`, `OSID`, `__Secure-1PSIDTS` |
| `linkedin` `cookies` | `fi.mau.linkedin.login.enter_cookies`, `https://linkedin.com/login` | required, all `request_header` on `https://www.linkedin.com`: `cookie_header` (`Cookie`), `x_li_track` (`X-LI-Track`), `x_li_page_instance` (`X-LI-Page-Instance`), each with a pattern. Optional: user agent (`request_header` or `special`), `sec-ch-ua`, `sec-ch-ua-platform`, `sec-ch-ua-mobile`, `sec-ch-prefers-color-scheme` | pattern set; JS returns `navigator.userAgent` only | absent | not applicable (the `Cookie` request header carries them) |
| `slack` `token` | `fi.mau.slack.login.enter_auth_token`, `https://slack.com/signin` | `auth_token`: required, pattern `^xoxc-.+$`, `special` `fi.mau.slack.auth_token` or `request_body` `token`. `cookie_token`: required, `cookie` `d` on `slack.com`, pattern `^xoxd-[a-zA-Z0-9/+=]+$` | none; JS polls `localStorage.localConfig_v2` and clicks "Use Slack in Browser" | absent | `d` |
| `groupme` `web` | `fi.mau.groupme.login.web`, `https://web.groupme.com/` | `token`: required, `local_storage` `access_token`, or `cookie` `token` on `.groupme.com`, or `request_header` `X-Access-Token` on `^https://api\.groupme\.com/v[34]/` | pattern set, no JS | absent | none named |
| `tumblr` `browser` | `com.ifixrobots.tumblr_dms.login.cookies`, `https://www.tumblr.com/messages` | required: `cookie_header` (`request_header` `Cookie`, or `special`), `sid` (`cookie`, no domain), `browser_user_agent` (`request_header` `user-agent`, with a pattern). Optional: cookies `pfu`, `logged_in`, `tmgioct`; `api_token` (`Authorization` header or `special`), `csrf_token` (`X-CSRF` or `X-CSRF-Token` header, or `special`), `api_version` (`X-Version` header) | pattern set; JS reads `document.cookie` and two tokens from the page | absent | `sid` |

Capture steps inside other flows:

| Network, flow | Step | Sources | `jsRequired` |
|---|---|---|---|
| `twitter` `password` | `browser_identity`, hidden, `https://x.com/robots.txt` | `browser_user_agent` (required) and the `sec-ch-ua` fields: `special` only | true |
| `twitter` `password` | `castle_token`, hidden, same URL | `castle_token` (required): `special` or `local_storage`. Browser headers: `request_header` on `^https://x\.com/` or `special` | absent. The local-storage value exists only after `extractJs` ran, so a client still needs JavaScript here. |
| `tumblr` `password` | `browser_verification`, hidden, `https://www.tumblr.com/login#...` | `blackbox_session_id` (required, `special`), `browser_user_agent` (`request_header`) | true |
| `slack` `email` | `email_captcha`, only when Slack asks | one required `special` field | true |
| `slack` `email` | `workspace_browser`, SSO workspaces | same fields as `token` | absent |
| `messenger` native flows | `recaptcha`, only when Meta asks | `recaptcha_token` (required, `special`) | true |
| `messenger` native flows | `google_oauth` | `oauth_token` (required, `special`) | true |
| `instagram` native flows | `web_captcha`, only when asked | `captcha_token` (required, `special`), `captcha_user_agent` (`special`) | true |
| `instagram` native flows | `web_challenge` | same cookie fields as `instagram` | absent |
| `linkedin` `password` | browser fallback | same step as `cookies` | absent |

What each value type needs from a client:

| Need | Networks |
|---|---|
| Cookie store only (httpOnly included) | `messenger`, `instagram`, `twitter` `cookies`, `gmessages` |
| Local storage (or a cookie, or a header) | `groupme` |
| Request body, or JavaScript on the page | `slack` `token` |
| Request headers (custom headers set by page JavaScript, and the `Cookie` header) | `linkedin`, `tumblr` `browser` |
| JavaScript in the provider page, no other source | `twitter` `password`, `tumblr` `password`, every captcha step |

Paste (PROTOCOL.md section 3b): a "Copy as cURL" command can give `cookie`, `request_header` and `request_body` values, so it can serve `messenger`, `instagram`, `twitter` `cookies`, `gmessages`, `linkedin`, `slack` `token` and `tumblr` `browser`. `groupme` `web` works too when the request has the `token` cookie or the `X-Access-Token` header. Steps with a required field that has only `local_storage` or `special` sources take a JSON object only.

Unverified: which cookies are httpOnly; every provider behaviour (Google Messages cookie rotation and Device Bound Session Credentials in Chrome are named in the upstream docs, not tested here).

All networks share one `maunium.net/go/mautrix` version: `v0.33.1-0.20261120111454-33abe66d85a0` (the highest that any pinned bridge requires; Go's minimum version selection picks it for all of them).

## messenger: Facebook Messenger

| Item | Value |
|---|---|
| Module | `go.mau.fi/mautrix-meta` `v0.2611.1-0.20261120174120-721148544bfb` (commit 721148544bfb, 2026-11-20), package `pkg/connector` (`MetaConnector`, the `cmd/mautrix-meta` binary) |
| Licence | AGPL-3.0 |
| Config / env | none. Upstream example config defaults. |
| Build | No extra requirement. go.mod needs `replace github.com/imroc/req/v3 => github.com/beeper/req/v3 v3.0.0-20261112020631-42bebc180e13`, copied from mautrix-meta's own go.mod: the upstream `imroc/req` v3.56.0 does not compile against `quic-go` v0.64.1 that mautrix-meta requires. |
| Binary | +14 MB (96 MB with WhatsApp and Telegram, 110 MB with Messenger) |

Flows (`GET /networks`):

| Flow id | Name | First step (kind, step id, payload) |
|---|---|---|
| `facebook` | facebook.com | `web_session_capture` `fi.mau.meta.cookies`: url `https://www.facebook.com/`, userAgent Chrome 157 Linux, fields `xs`, `c_user`, `datr` (each required, one source `cookie` with cookieDomain `facebook.com`), waitForUrlPattern `^https://www\.facebook\.com/(?:messages/(?:e2ee/)?(?:t/[0-9]+/?)?)?(?:\?.*)?$` |
| `messenger` | messenger.com | `web_session_capture` `fi.mau.meta.cookies`: url `https://www.messenger.com/?no_redirect=true`, same three cookies with cookieDomain `messenger.com`, waitForUrlPattern `^https://www\.messenger\.com/(?:e2ee/)?(?:t/[0-9]+/?)?(?:\?.*)?$` |
| `messenger-lite` | Messenger iOS | `form_input` `fi.mau.meta.messengerlite.email_password`: fields `username` (type `username`), `password` (type `password`) |
| `messenger-lite-android` | Messenger Android | same as `messenger-lite` |

Later steps, from source (not observed): after cookies, an optional `fi.mau.meta.history_pin` (one optional `password` field, 6 digits; becomes `form_input` because the field is optional). The native flows are driven by Meta's Bloks screens: `code_input` (SMS, WhatsApp, TOTP, backup codes), `form_input` with `select` fields (MFA method, contact point, dialogs), `form_input` with `captcha_code` and an image or audio attachment, `external_approval_wait` (approve on a trusted device), and a reCAPTCHA `web_session_capture` step with `extractJs` and a `special` source `recaptcha_token` (`jsRequired: true`). A client without a web view cannot complete that reCAPTCHA step.

Unverified: anything after the first step. Meta checkpoints from a datacenter IP, passkey-only accounts ("mandatory passkey" is not supported upstream), E2EE history restore, session lifetime.

## instagram: Instagram

| Item | Value |
|---|---|
| Module | same `go.mau.fi/mautrix-meta` commit 721148544bfb, package `pkg/igconnector` (`IGConnector`, the `cmd/mautrix-instagram` binary). A separate network id with its own `instagram.db` and logins; it shares no state with `messenger`. |
| Licence | AGPL-3.0 |
| Config / env | none. Upstream example config defaults. |
| Build | No extra requirement beyond the Messenger `replace`. |
| Binary | +2 MB (112 MB) |

| Flow id | Name | First step |
|---|---|---|
| `instagram` | Cookies | `web_session_capture` `fi.mau.meta.cookies`: url `https://www.instagram.com/accounts/login/`, no userAgent, required cookies `sessionid`, `csrftoken`, `ds_user_id`, optional `rur`, `shbid`, `shbts`, `mid`, `ig_did` (cookieDomain `instagram.com`), waitForUrlPattern `^https://www\.instagram\.com/(?:direct/(?:inbox/\|t/[0-9]+/)?\|accounts/onetap/)?(?:\?.*)?$` |
| `instagram-password` | Native Web | `form_input` `fi.mau.meta.instagram.credentials`: `username` (type `username`, autofillDomain `instagram.com`), `password` |
| `android` | Native Android | same step as `instagram-password` |

Later steps, from source (not observed): `code_input` (two-factor), `external_approval_wait` or `pairing_code` (approve on another device, sometimes with a code), `form_input` with `select` (verification method, inbox retry), `web_session_capture` with `extractJs` for a captcha (`jsRequired`) or a web challenge. The browser steps need a client with a browser or web view.

Unverified: anything after the first step, checkpoints, session lifetime.

## signal: Signal (optional, build tag `signal`)

| Item | Value |
|---|---|
| Module | `go.mau.fi/mautrix-signal` `v0.2611.1-0.20261118112413-11ede28800d5` (commit 11ede28800d5, 2026-11-18), package `pkg/connector` (`SignalConnector`) |
| Licence | AGPL-3.0, plus libsignal (AGPL-3.0, Rust, statically linked) |
| Config / env | none. Device name set to `Roomcord` (upstream default `mautrix-signal`); it shows in the user's Signal app under linked devices. |
| Build | Not in the default build. `pkg/libsignalgo` links `-lsignal_ffi` with cgo, so `internal/networks/signal.go` has `//go:build signal`. Without the tag, `BRIDGE_HOST_NETWORKS=signal` starts the host and `/networks` lists `signal` with `available: false` and `reason: "this binary was built without the signal build tag ..."`. |
| Binary | 178 MB with the tag (112 MB without). |

Build steps, as run on macOS arm64 (rustc 1.99.1 from rustup, cmake, Xcode clang). No system-wide install: protoc is a release zip in a scratch directory.

```
# 1. libsignal at the commit that mautrix-signal pins as its submodule (v0.106.1)
git init libsignal && cd libsignal
git remote add origin https://github.com/signalapp/libsignal.git
git fetch --depth 1 origin 904c8dc8216073b078a5f2f5090e3da8382e6a92 && git checkout FETCH_HEAD
# 2. protoc on PATH (rust-protobuf looks it up there; PROTOC alone is not enough)
curl -LO https://github.com/protocolbuffers/protobuf/releases/download/v29.3/protoc-29.3-osx-aarch_64.zip   # sha256 2b8a3403...8765
unzip protoc-29.3-osx-aarch_64.zip -d ../protoc
# 3. the static library (a few minutes cold; libsignal pins toolchain 1.98.1, 1.99.1 also builds it)
PATH=$PWD/../protoc/bin:$PATH RUSTUP_TOOLCHAIN=1.99.1 RUSTFLAGS="-Ctarget-feature=-crt-static" RUSTC_WRAPPER="" \
  cargo build -p libsignal-ffi --profile=release          # -> target/release/libsignal_ffi.a, 135 MB
# 4. the host
make build-signal LIBSIGNAL_DIR=<libsignal>/target/release   # in the roomcord-bridge-host directory
```

Upstream also links prebuilt `libsignal_ffi.a` files (mau.dev `tulir/gomuks-build-docker` CI artifacts, `pkg/libsignalgo/README.md`). They follow libsignal master, not the pinned v0.106.1, so they were not used.

| Flow id | Name | First step |
|---|---|---|
| `qr` | QR | `qr` `fi.mau.signal.login.qr`, pollable: data `sgnl://linkdevice?capabilities=backup5%2Cnopni2&pub_key=...` (about 140 characters) |

Later steps, from source: the QR refreshes every 45 s (at most 20 times), then `done`. `go test -tags signal ./internal/networks` starts the Signal bridge on a fresh database.

Unverified: scanning the QR, the same-phone `sgnl://` link, device limits, everything after the first step. The licences of libsignal's Rust dependencies.

## slack: Slack (personal session)

| Item | Value |
|---|---|
| Module | `go.mau.fi/mautrix-slack` `v0.2611.1-0.20261120192952-6553d937cc8d` (commit 6553d937cc8d, 2026-11-20), package `pkg/connector` (`SlackConnector`) |
| Licence | AGPL-3.0 |
| Config / env | none. Upstream example config defaults. |
| Build | go.mod needs `replace github.com/slack-go/slack => github.com/beeper/slackgo v0.0.0-20261117191942-d0ae74c35f48`, copied from mautrix-slack's go.mod (the bridge uses Beeper's fork API). |
| Binary | +3 MB (115 MB) |

This is a personal Slack session of the user (browser token `xoxc-` plus cookie `d`, or the email-code login). It is not a Slack app install in a workspace.

| Flow id | Name | First step |
|---|---|---|
| `email` | Email | `form_input` `fi.mau.slack.login.enter_email`: one field `email` (type `email`, pattern `^[^@\s]+@[^@\s]+\.[^@\s]+$`, autofillDomain `slack.com`) |
| `token` | Auth token & cookie | `web_session_capture` `fi.mau.slack.login.enter_auth_token`: url `https://slack.com/signin`, `extractJs` (about 800 characters, reads the token from `localStorage.localConfig_v2`), no `jsRequired` (the token also has a `request_body` source); fields `auth_token` (pattern `^xoxc-.+$`, sources `special` `fi.mau.slack.auth_token`, or `request_body` `token` on `^https://.+?\.slack\.com/api/(client\|experiments\|api\|users\|teams\|conversations)\..+$`) and `cookie_token` (pattern `^xoxd-[a-zA-Z0-9/+=]+$`, cookie `d` on `slack.com`) |
| `app` | Slack app | `form_input` `fi.mau.slack.login.enter_app_tokens`: `bot_token` (`^xoxb-.+$`), `app_token` (`^xapp-.+$`), both type `token` |

Later steps of `email`, from source: an optional captcha `web_session_capture` (`special` source, `extractJs`, `jsRequired: true`), `code_input` for the emailed code, `form_input` with a `select` of workspaces (skipped when there is one), an optional second-factor `code_input`, or a `web_session_capture` of the workspace in a browser for SSO workspaces. The browser steps need a client with a browser or web view.

Unverified: anything after the first step, SSO workspaces, Slack Enterprise Grid, token lifetime.

## linkedin: LinkedIn

| Item | Value |
|---|---|
| Module | `go.mau.fi/mautrix-linkedin` `v0.2611.1-0.20261120174201-6a016fbdf6e3` (commit 6a016fbdf6e3, 2026-11-20), package `pkg/connector` (`LinkedInConnector`) |
| Licence | AGPL-3.0 |
| Config / env | none. Upstream example config defaults. |
| Build | No extra requirement. |
| Binary | +1 MB (116 MB) |

| Flow id | Name | First step |
|---|---|---|
| `cookies` | Cookies | `web_session_capture` `fi.mau.linkedin.login.enter_cookies`: url `https://linkedin.com/login`, waitForUrlPattern `^https://www\.linkedin\.com/feed.*$`, `extractJs` (81 characters, returns the user agent), no `jsRequired` (every required field is a request header). Required fields from request headers on `https://www.linkedin.com`: `cookie_header` (`Cookie`, pattern `\bJSESSIONID=[^;]+`), `x_li_track` (`X-LI-Track`), `x_li_page_instance` (`X-LI-Page-Instance`). Optional: `User-Agent` (header or `special`), `Sec-Ch-Ua`, `Sec-Ch-Ua-Platform`, `Sec-Ch-Ua-Mobile`, `Sec-Ch-Prefers-Color-Scheme`. Field ids are prefixed `fi.mau.linkedin.login.` |
| `password` | Email and password | `form_input` `fi.mau.linkedin.login.credentials`: `identifier` (type `username`), `password` (both autofillDomain `linkedin.com`) |

Later steps of `password`, from source: `code_input` (email, SMS or TOTP code), `form_input` `select` of an approval method, `external_approval_wait` (approve in the LinkedIn app, `can_cancel`), or a `browser_fallback` select followed by the cookie capture. The browser fallback needs a client with a browser or web view. LinkedIn rejects cookies used with another user agent (upstream docs), so the captured `User-Agent` matters.

Upstream can send the password login's HTTP traffic through the client (`LoginStartParams.HTTP`, the provisioning API's `client_http` opt-in). The host does not opt in, so upstream calls LinkedIn from the host's IP.

Unverified: anything after the first step, LinkedIn's reaction to a datacenter IP, session lifetime.

## twitter: X

| Item | Value |
|---|---|
| Module | `go.mau.fi/mautrix-twitter` `v0.2609.1-0.20261120174138-77b2290c8537` (commit 77b2290c8537, 2026-11-20), package `pkg/connector` (`TwitterConnector`). The network id stays `twitter` as upstream names it. |
| Licence | AGPL-3.0 |
| Config / env | none. Upstream example config defaults. |
| Build | Uses `imroc/req`; the `beeper/req` fork that the Messenger `replace` pins (2026-11-12) is newer than the one mautrix-twitter pins (2026-08-08) and builds. `go get` raised `PuerkitoBio/goquery` v1.10.3 to v1.13.0 and `andybalholm/cascadia` v1.3.3 to v1.3.4 (minimum version selection, no bridge pin moved). |
| Binary | +3 MB (120 MB) |

| Flow id | Name | First step |
|---|---|---|
| `cookies` | Cookies | `web_session_capture` `fi.mau.twitter.login.enter_cookies`: url `https://x.com/i/flow/login`, required cookies `ct0` and `auth_token` (no cookieDomain), no waitForUrlPattern |
| `password` | Username/password | `form_input` `fi.mau.twitter.login.enter_credentials`: `identifier` (type `username`, autofillDomain `x.com`), `password` |

After the credentials, upstream always asks for a hidden `web_session_capture` (`fi.mau.twitter.login.browser_identity`: `x.com/robots.txt`, `extractJs`, `special` sources for the browser headers, `jsRequired: true`), then `external_approval_wait` (`prepare`), then another hidden capture for X's Castle anti-bot token, so the password flow needs a client with a browser too. Other later steps, from source: `code_input` or `form_input` for verification, a `select` of the second-factor method (a passkey/security-key option is listed as unsupported upstream), and `code_input` for the encrypted-DM PIN (`juicebox_pin`, also after the cookie flow). Upstream can route the password login's X requests through the client (`LoginStartParams.HTTP`); the host does not opt in.

Unverified: anything after the first step, Castle token handling from the host's IP, PIN recovery, session lifetime.

## gmessages: Google Messages

| Item | Value |
|---|---|
| Module | `go.mau.fi/mautrix-gmessages` `v0.2611.1-0.20261119194137-ed5dc19eb2b4` (commit ed5dc19eb2b4, 2026-11-19), package `pkg/connector` (`GMConnector`) |
| Licence | AGPL-3.0 |
| Config / env | none. Upstream example config defaults. |
| Build | No extra requirement. It declares `maunium.net/go/mautrix v0.33.0`; the host uses the newer shared pin and it builds. |
| Binary | +3 MB (122 MB) |

| Flow id | Name | First step |
|---|---|---|
| `google` | Google Account | `web_session_capture` `fi.mau.gmessages.google_account`: url `https://accounts.google.com/AccountChooser?continue=https://messages.google.com/web/config`, required cookies `SID`, `HSID`, `SSID`, `APISID`, `SAPISID` (cookieDomain `.google.com`) and `OSID` (`messages.google.com`), optional `__Secure-1PSIDTS` |

The QR pairing flow exists in source but is not in `GetLoginFlows` upstream (Google removed it), so it is not offered. The cookie step needs a client with a browser. Later steps, from source: `external_approval_wait` with `emoji` and `imageUrl` (an SVG of the emoji; the user taps the same emoji in Google Messages on the Android phone), then `done`.

Unverified: anything after the first step. Upstream docs warn that the browser rotates Google cookies (use a private window) and that Chrome's device-bound session credentials break reuse; pairing must be enabled in the phone's Google Messages.

## line: LINE

| Item | Value |
|---|---|
| Module | `github.com/beeper/line` `v0.0.0-20261120193203-6b8d57b4e113` (commit 6b8d57b4e113, 2026-11-20), package `pkg/connector` (`LineConnector`) |
| Licence | MIT |
| Config / env | none. Upstream config `qr_login: false` (default) keeps the QR flow off. |
| Build | No extra build requirement. `internal/localmatrix` now implements `MatrixConnectorWithServer` (`GetPublicAddress` returns "", `GetRouter` returns a mux that is never served): LINE's `Start` refuses to run without that interface but never calls it. Runtime: media conversion runs the `ffmpeg`, `heif-info` and `heif-dec` binaries when they are on `PATH` (video thumbnails, iPhone HEIF photos); not installed or tested here. `ffmpeg-go` pulls in `aws-sdk-go` (+21 MB). |
| Binary | +21 MB (143 MB) |

| Flow id | Name | First step |
|---|---|---|
| `dev.highest.matrix.line.email_login` | Login | `form_input` `dev.highest.matrix.line.enter_creds`: `email` (type `username`, autofillDomain `line.me`), `password` |
| `dev.highest.matrix.line.qr_login` (only with `qr_login: true`) | QR Code | not listed with the default config |

Later steps, from source: `external_approval_wait` (`wait_verification`, or `enter_pin` with the PIN in `instructions`; the user types it in the LINE app), then `done`. The QR flow would be `qr` then `pairing_code` (`qr_pin`).

Unverified: anything after the first step. The upstream README says that LINE allows one such session per account: a login elsewhere (the Chrome extension) ends the bridge session.

## groupme: GroupMe

| Item | Value |
|---|---|
| Module | `github.com/beeper/groupme` `v0.0.0-20261120193234-1bbba535abb9` (commit 1bbba535abb9, 2026-11-20), package `pkg/connector` (`GMConnector`); client library `github.com/beeper/groupme-lib` |
| Licence | AGPL-3.0 (bridge), GPL-3.0 (`groupme-lib`) |
| Config / env | none. Upstream example config defaults. |
| Build | No extra requirement. |
| Binary | +1 MB (144 MB) |

| Flow id | Name | First step |
|---|---|---|
| `web` | GroupMe | `web_session_capture` `fi.mau.groupme.login.web`: url `https://web.groupme.com/`, waitForUrlPattern `^https://web\.groupme\.com/(?:[^?#]*)(?:[?#].*)?$`, one field `token` from localStorage `access_token`, cookie `token` (`.groupme.com`) or request header `X-Access-Token` on `^https://api\.groupme\.com/v[34]/` |
| `password` | GroupMe password | `form_input` `fi.mau.groupme.login.credentials`: `username` (type `username`), `password` (autofillDomain `groupme.com`) |
| `access-token` | GroupMe access token | `form_input` `fi.mau.groupme.login.enter_token`: one field `token` (type `token`) |

Later steps of `password`, from source: `code_input` `fi.mau.groupme.login.verify`, then `done`. Accounts that sign in with Google, Microsoft or Apple must first set a GroupMe password (upstream instructions); upstream warns that Google blocks embedded login windows for `web`.

Unverified: anything after the first step, token lifetime.

## tumblr: Tumblr

| Item | Value |
|---|---|
| Module | `github.com/beeper/tumblr-dms` `v0.1.1-0.20261120193536-78a2515b6853` (commit 78a2515b6853, 2026-11-20), package `pkg/connector` (`TumblrConnector`) |
| Licence | AGPL-3.0 or later; `LICENSE.exceptions` grants extra rights to Beeper and Element only. Pulls in `github.com/beeper/push-receiver` (MIT per README; no LICENSE file in the module). |
| Config / env | none. Upstream example config defaults. |
| Build | No extra requirement. |
| Binary | +2 MB (146 MB) |

| Flow id | Name | First step |
|---|---|---|
| `browser` | Browser | `web_session_capture` `com.ifixrobots.tumblr_dms.login.cookies`: url `https://www.tumblr.com/messages`, waitForUrlPattern `^https://www\.tumblr\.com/(?:messages\|messaging\|dashboard\|blog/[^/?#]+/messages)(?:[/?#].*)?$`, `extractJs` (about 940 characters), no `jsRequired` (every required field has a cookie or header source). Required: `cookie_header` (request header `Cookie` or `special`), cookie `sid`, `browser_user_agent` (request header `user-agent`). Optional: cookies `pfu`, `logged_in`, `tmgioct`; `api_token` (`Authorization` or `special`), `csrf_token` (`X-CSRF`, `X-CSRF-Token` or `special`), `api_version` (`X-Version`) |
| `password` | Email & password | `form_input` `com.ifixrobots.tumblr_dms.login.credentials`: `identifier` (type `email`, pattern `^[^@\s]+@[^@\s]+\.[^@\s]+$`), `password` (autofillDomain `tumblr.com`) |

After the credentials (and after a second-factor code) upstream always asks for a hidden `web_session_capture` (`browser_verification`: a Tumblr Blackbox session id from `extractJs` and the browser user agent, `jsRequired: true`), so the password flow needs a client with a browser too. Other later steps, from source: `code_input` (`two_factor`), `form_input` `select` of the blog (`blog`), then `done`. The browser flow also works for Google and Apple sign-in accounts.

Unverified: anything after the first step.
