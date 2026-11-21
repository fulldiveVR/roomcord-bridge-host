# Bridge host wire protocol

This document defines the HTTP/JSON and WebSocket API of `roomcord-bridge-host`. A client of the host implements exactly this document.

## 1. Transport

- The host listens on `127.0.0.1` (`BRIDGE_HOST_ADDR`). Every request carries `Authorization: Bearer <BRIDGE_HOST_TOKEN>`, also the WebSocket upgrade. A missing or wrong token answers `401 unauthorized`.
- Bodies are JSON. Times are RFC 3339 / ISO 8601 strings in UTC.
- Errors: `{ "error": { "code": "lower_snake", "message": "..." } }` with a 4xx or 5xx status. A rejected WebSocket upgrade carries the same body.

A client should map an unknown code to `internal_error` and keep the host code in the message. A 502, 503 or 504 without an error body, a refused connection and a timeout mean that the host is unreachable.

| Code | Status | When |
|---|---|---|
| `unauthorized` | 401 | Bad token. |
| `invalid_input` | 400 | Bad JSON, missing field, bad cursor, missing multipart field `file`. |
| `account_not_found` | 404 | Unknown `loginId`, also after logout. A client can treat it as `session_revoked` on data calls and `health`, and as success on `logout` and `DELETE /data`. |
| `session_revoked` | 409 | The login exists but its state is `needs_reauth` (data calls: conversations, history, send, read, media upload). |
| `connect_session_not_found` | 404 | Unknown, cancelled or expired login process. |
| `network_not_found` | 404 | Unknown network on `POST /logins/start`. |
| `network_unavailable` | 503 | Network not available (`available: false`: config missing or invalid, or not built into this binary), for every flow. Also `account_status.code` for upstream `UNKNOWN_ERROR`. |
| `stale_step` | 409 | Submit for a step that is not current, or while another submit runs. |
| `conversation_not_found`, `message_not_found`, `attachment_not_found` | 404 | Unknown ids under the login. |
| `unsupported_operation` | 422 | More than one `uploadRef` on a send. |
| `attachment_too_large` | 413 | Upload over 100 MiB. |
| `account_not_connected` | 502 | Upstream says the login is not connected while sending. |
| `internal_error` | 500 | Anything else. |

## 2. Endpoints

| Method and path | Request | Response |
|---|---|---|
| `GET /health` | | `{ ok, version, networks: [id] }` |
| `GET /networks` | | `[{ id, displayName, available, reason?, flows: [{ id, name, description }], capabilities: { disappearingMessages?, implicitReadReceipts?, loginReady, configError? } }]` (section 3a) |
| `POST /logins/start` | `{ network, userId, flowId, reconnectLoginId? }` | `{ processId, step }` |
| `GET /logins/{processId}` | | `{ processId, step }`. Never blocks: the host runs upstream `Wait` in the background and returns the cached step. |
| `POST /logins/{processId}/submit` | `{ stepId, fields?: {id: value}, cookies?: {id: value} }` | `{ processId, step }` |
| `DELETE /logins/{processId}` | | 204. A later `GET` is 404 `connect_session_not_found`. |
| `GET /accounts?userId=` | | `[{ loginId, network, userId, remoteId, remoteName, state }]` |
| `WS /accounts/{loginId}/events?cursor=<seq>` | | Frames, section 6 |
| `GET /accounts/{loginId}/conversations?cursor&limit` | `cursor` omitted on the first page | `{ items: Conversation[], nextCursor }` |
| `POST /accounts/{loginId}/history` | `{ conversationId, cursor?: string or null, limit }` | `{ items: Message[], nextCursor }`, newest first |
| `POST /accounts/{loginId}/send` | `{ conversationId, clientMessageId, text?, uploadRefs?: [id], replyToMessageId? }` | `{ externalMessageId, sentAt, attachments? }` |
| `POST /accounts/{loginId}/read` | `{ conversationId, upToMessageId? }` | 204 |
| `POST /accounts/{loginId}/media` | multipart, field `file` (file name and type from the part headers) | `{ uploadRef }` |
| `GET /accounts/{loginId}/media/{messageId}/{attachmentId}?conversationId=` | | bytes, `Content-Type`, `Content-Disposition` (section 9) |
| `GET /accounts/{loginId}/health` | | `{ state, since, detail? }` |
| `POST /accounts/{loginId}/logout` | | 204 |
| `DELETE /accounts/{loginId}/data` | | 204 |

`conversations`: `cursor` is an opaque offset, `limit` defaults to 50, max 200. `history`: `limit` defaults to 50, max 200; the cursor is opaque (`<sentAtMillis>|<externalMessageId>` today). Neither response has `participants` today; a client should accept an optional `participants` array on both and use `[]` when absent.

`send` is idempotent on `clientMessageId` per login: a repeat, also a concurrent one, returns the first result (including `attachments`) and sends one network message. The sent message also arrives once as an outbound `message_upsert`. `attachments` is present only when the message has one; the client needs it to download the media later.

`read` without `upToMessageId` marks the newest bridged message. The host forwards the receipt to the network; it does not echo a `read_receipt` event for it.

## 3. Login steps

Step object: `{ stepId, kind, instructions?, expiresAt?, pollable, error?: { code, message, attemptsLeft?, retryAfter?, upstreamCode? }, ...payload }`.

| bridgev2 step | `kind` | payload |
|---|---|---|
| `display_and_wait` type `qr` | `qr` | `data`, pollable. A refresh keeps the `stepId` and changes `data`. |
| `display_and_wait` type `code` | `pairing_code` | `code`, pollable |
| `display_and_wait` type `emoji` / `nothing` | `external_approval_wait` | `emoji?` (its own field, never in `instructions`), `imageUrl?`, pollable. The user picks or confirms in the other app (Google Messages emoji, Meta approval, LINE PIN in `instructions`). |
| `user_input`, single `phone_number` field | `phone_number_input` | `fieldId` |
| `user_input`, single `2fa_code` field | `code_input` | `fieldId` |
| `user_input`, single `password` field | `password_input` | `fieldId` |
| `user_input`, single `email` field, a single optional field, a step with attachments, or anything else | `form_input` | `fields: [{ id, type, name, description?, pattern?, options?, defaultValue?, placeholder?, autofillDomain?, optional?, minLength?, maxLength? }]`, `attachments?: [{ type, fileName?, mimeType?, width?, height?, dataBase64 }]` (captcha image or audio). `type` is the bridgev2 field type: `username`, `password`, `phone_number`, `email`, `2fa_code`, `token`, `url`, `domain`, `select` (with `options`), `captcha_code`. |
| `cookies` | `web_session_capture` | `url, userAgent?, fields: [{ id, required, pattern?, sources: [{ type, name, cookieDomain?, requestUrlRegex? }] }], extractJs?, waitForUrlPattern?, initialCookies?: [{ name, value, domain, path?, secure?, httpOnly? }], hidden?, jsRequired?`. Source `type`: `cookie`, `local_storage`, `request_header`, `request_body`, `special` (a value only provider-page JavaScript makes, for example a captcha token from `extractJs`). `hidden`: capture without showing the page. `jsRequired`: set when upstream says so, when `extractJs` is set, or when a source is `special`. |
| `complete` | `done` | `loginId`, `remoteId`, `remoteName` |
| `webauthn`, `client_http`, an unknown display type, anything else | `failed` | `error.code = unsupported_method` |

Rules:

- An upstream retry step (for example Telegram `…code.incorrect`) is the same input step with `error` set. Other upstream errors end with `failed`.
- `error.upstreamCode` carries the raw upstream code. Clients may ignore it.
- The host validates input with the upstream field validators first. Invalid input returns the same step with `error` and never reaches upstream.
- A submit for a `stepId` that is not current, or while a submit runs, is `409 stale_step`. A submit after `done` or `failed` returns that final step.
- Each upstream `Start` and `Submit` call runs at most 60 s; on the cap the process ends with `failed` (`expired` or the upstream code). A process lives at most 30 min. Finished processes stay readable for 10 min; cancelled ones are removed at once.
- `expiresAt` is never set: upstream gives no expiry. WhatsApp rotates the QR after 60 s, then every 20 s, and fails with `expired` after the last code. The client shows now + 60 s (QR) or now + 180 s (pairing code).
- Login error codes: `invalid_phone_number`, `invalid_code`, `invalid_password`, `invalid_input`, `rate_limited`, `expired`, `cancelled`, `unsupported_method`, `internal_error`. A client maps an unknown code on a retry step to `invalid_input` and on `failed` to `internal_error`.
- A network with `available: false` answers `503 network_unavailable` on `POST /logins/start` for every flow (Telegram without `TELEGRAM_API_ID` and `TELEGRAM_API_HASH`). A client should check `available` first and show the `reason`. The host never waits on the network in this case.
- Not served, they end with `failed` `unsupported_method`: `webauthn` (only mautrix-whatsapp emits it, for accounts that WhatsApp asks for a passkey; X lists a security-key method that upstream itself marks unsupported), `client_http` (the client runs the provider request; LinkedIn and X can use it only when the provisioning client opts in, which the host does not), and the `can_cancel` back action of `display_and_wait` and `user_input` steps (LinkedIn app approval; the host has no route for `CancelStep`, `DELETE /logins/{id}` ends the whole process).
- A `web_session_capture` step needs a client that can show the provider page in a browser or web view and read cookies, headers or local storage from it. A client without one must cancel the login process at that step.

## 3a. Network availability

`GET /networks` lists every network named in `BRIDGE_HOST_NETWORKS`:

- `available: true`: the network runs and its config is valid.
- `available: false` with `reason`: the host cannot log in to it. Cases: a required env variable is unset (`reason: "missing configuration: TELEGRAM_API_ID, TELEGRAM_API_HASH"`); the connector's own `ValidateConfig` failed (`reason` is its message); the network needs a build tag that this binary lacks (`reason` names the tag; `flows` is empty and `displayName` is the id). `capabilities.loginReady` is `false` and `capabilities.configError` repeats `reason`.
- An id that the binary does not know at all stops the host at start (`Unknown network in BRIDGE_HOST_NETWORKS`).

## 4. Flow ids

Upstream flow ids at the pinned commits. Per-network details (first steps observed, config, build): [docs/networks.md](docs/networks.md).

| Network | Flow id | Steps |
|---|---|---|
| WhatsApp | `qr` | `qr` (refreshing), `done` |
| WhatsApp | `phone` | `phone_number_input`, `pairing_code`, `done` |
| Telegram | `phone` | `phone_number_input`, `code_input`, `password_input?`, `done` |
| Telegram | `qr` | `qr`, `password_input?`, `done` |
| Telegram | `bot`, `manual` | Upstream flows, not exercised by the host tests |
| Signal | `qr` | `qr` (refreshing), `done`. Build tag `signal` only. |
| Slack | `email` | `form_input` (email), `code_input`, then any of `form_input` (workspace), `code_input` (2FA), `web_session_capture` (captcha, SSO), `done` |
| Slack | `token` | `web_session_capture` (token and cookie `d`), `done` |
| Slack | `app` | `form_input` (bot and app tokens). A workspace bot, not a user account. |
| Messenger | `messenger`, `facebook` | `web_session_capture`, `form_input?` (optional backup PIN), `done` |
| Messenger | `messenger-lite`, `messenger-lite-android` | `form_input` (username, password), then any of `code_input`, `form_input`, `external_approval_wait`, `web_session_capture` (reCAPTCHA), `done` |
| Instagram | `instagram` | `web_session_capture`, `done` |
| Instagram | `instagram-password`, `android` | `form_input`, then any of `code_input`, `form_input`, `external_approval_wait`, `pairing_code`, `web_session_capture`, `done` |
| LinkedIn | `cookies` | `web_session_capture` (cookie and request headers), `done` |
| LinkedIn | `password` | `form_input`, then any of `code_input`, `form_input`, `external_approval_wait`, `web_session_capture`, `done` |
| X (`twitter`) | `cookies` | `web_session_capture`, `code_input?` (DM PIN), `done` |
| X (`twitter`) | `password` | `form_input`, hidden `web_session_capture` (browser identity), `external_approval_wait`, hidden `web_session_capture` (Castle token), then any of `code_input`, `form_input`, `done` |
| Google Messages | `google` | `web_session_capture` (Google cookies), `external_approval_wait` (emoji, `imageUrl`), `done` |
| LINE | `dev.highest.matrix.line.email_login` | `form_input` (email, password), `external_approval_wait` (PIN in `instructions`), `done` |
| GroupMe | `web` | `web_session_capture` (access token), `done` |
| GroupMe | `password` | `form_input`, `code_input?`, `done` |
| GroupMe | `access-token` | `form_input` (token), `done` |
| Tumblr | `browser` | `web_session_capture`, `form_input?` (blog), `done` |
| Tumblr | `password` | `form_input`, hidden `web_session_capture` (Blackbox), `code_input?`, `form_input?` (blog), `done` |
| fake | `phone` | `phone_number_input` (field `phone_number`), `code_input` (field `code`, value `111111`), `done` |

## 5. Identifiers and accounts

- `loginId` is `<network>:<upstream user login id>`, for example `whatsapp:15551234567`, `fake:15550001111`. `remoteId` is the part after the first colon.
- `externalConversationId` is the upstream portal id. Portals are split per login (`split_portals: true`), so two users never share a conversation, a message, an event or a media file.
- `externalMessageId` is the upstream message id. A message that upstream splits into parts gets `<messageId>#<partId>` per part; a single part keeps the plain id.
- `externalParticipantId` is the upstream user id. The account's own user has `isSelf: true`.
- Account `state` (in `/accounts`, `/health` and `account_status.status`): `connected` (bridgev2 `CONNECTED`, `BACKFILLING`), `connecting` (`CONNECTING`, `TRANSIENT_DISCONNECT`), `needs_reauth` (`BAD_CREDENTIALS`, `LOGGED_OUT`), `error` (`UNKNOWN_ERROR` and others). The host reads the newest state that the connector queued, so `health` is current right after a login or a revoke.

## 6. Event stream

`WS /accounts/{loginId}/events?cursor=<seq>`. Frame: `{ "seq": number, "type": string, "payload": object }`.

- `seq` is per login, starts at 1, increases by 1 and is stored in SQLite with the event (outbox). It survives a host restart and is never reused, also not after `DELETE /data`.
- Delivery is at least once. The cursor is the last processed `seq` as a decimal string. A client that reconnects with its cursor gets every later frame; it may get a frame twice.
- No cursor (or `null`): the stream starts at the head and first sends `{ seq: <head>, type: "checkpoint", payload: {} }`. The checkpoint is not stored; its `seq` is a valid cursor.
- One subscriber per login. A new subscriber closes the older socket with close code `4000`. A client must not reconnect after close 4000: the newer consumer owns the stream, and two consumers must not replace each other in a loop. After any other close or a network error, a client should wait and subscribe again with its cursor.
- `account_status` frames are state, not history: the host skips a stored `account_status` frame whose status is no longer the current state of the login (for example a `needs_reauth` that a later re-login replaced). The newer status frame is delivered instead.
- The host pings an idle socket every 25 s.

Wire event names and payloads:

| Wire `type` | Payload |
|---|---|
| `checkpoint` | `{}` |
| `conversation_upsert` | `{ conversation: Conversation, participants?: Participant[] }` |
| `participant_upsert` | `{ externalConversationId, participant: Participant }`. The conversation view already lists the id, so a client may ignore it. |
| `message_upsert` | `{ message: Message, participants?: Participant[] }`. Also edits: the same `externalMessageId` with `editedAt` and the new text. |
| `message_delete` | `{ externalConversationId, externalMessageId, deletedAt }` |
| `reaction_upsert`, `reaction_delete` | `{ externalConversationId, externalMessageId, reaction: Reaction, reactions: Reaction[] }`. `reaction` is the change, `reactions` the full list of the message after it. |
| `read_receipt` | `{ externalConversationId, lastReadExternalMessageId, readerExternalId, isSelf, unreadCount }`. `unreadCount` (inbound bridged messages after the read one) is set only when `isSelf`. |
| `account_status` | `{ status: connected or connecting or needs_reauth or error, code: ErrorCode or null, detail? }`. `needs_reauth` carries `session_revoked`, `error` carries `network_unavailable` or `internal_error`. |
| `message_status` | `{ externalConversationId, externalMessageId, status: sent or delivered or read or failed }`. Reserved: the host does not emit it yet. |

Shapes:

```ts
Conversation: { network, subNetwork: null, externalConversationId, kind: 'dm'|'group', title, avatarUrl: null, participantExternalIds: string[], unreadCount: number|null, lastReadExternalMessageId: string|null }
Participant:  { network, externalParticipantId, displayName, handle: null, avatarUrl: null, isSelf }
Message:      { network, externalConversationId, externalMessageId, direction: 'inbound'|'outbound', senderExternalId, senderName: string|null, text, subject: null, replyToExternalMessageId: string|null, attachments: Attachment[], reactions: Reaction[], status: 'delivered'|'sent', sentAt, editedAt: string|null, deletedAt: string|null }
Attachment:   { network, externalAttachmentId, kind: 'image'|'video'|'audio'|'voice'|'file'|'sticker', mimeType, fileName, sizeBytes, width, height, durationMs }
Reaction:     { network, senderExternalId, emoji, reactedAt: string|null }
```

Reactions compare emoji without variation selectors (bridgev2 adds U+FE0F on some paths only). A conversation's `lastReadExternalMessageId` and `unreadCount` are filled from the account's own read receipts; before the first one they are null.

## 7. History

`POST /history` pages the messages that the host has already bridged: live events plus the upstream forward backfill when a chat is first created (configured to 50 messages; the backfill size is not verified with a real account). It does not ask the network for older pages on demand. Pages are newest first; `nextCursor` is null on the last page; the same cursor gives the same page. History is therefore partial for every network. An unknown conversation is `404 conversation_not_found`.

## 8. Session end and data deletion

- `POST /logout` calls upstream logout including the remote revocation and deletes the login. Afterwards every account call for that `loginId` is `404 account_not_found`, which a client can treat as `session_revoked` (data calls) and as success (`logout` again, `DELETE /data`).
- A session that the network ended (`BAD_CREDENTIALS`, `LOGGED_OUT` without a logout from us) keeps the login: `account_status needs_reauth`, data calls `409 session_revoked`, `health` `needs_reauth`. A new login of the same account with `reconnectLoginId` reuses it and the stream goes on from the same cursor.
- `DELETE /data` deletes every local copy: the login's portals and messages in the bridgev2 database, its outbox, conversation and message views, send records and media files. It does NOT end the session, and `health` stays unchanged. It keeps the per-login seq counter. It is idempotent and works after logout.

## 9. Media

- Upload: `POST /media` stores the file under the login and returns `uploadRef`. Send it in `uploadRefs` (at most one per message; the text becomes the caption). The send response has the resulting attachment; its `externalAttachmentId` is the `uploadRef`.
- Incoming media is stored by the host when the bridge converts the message; the attachment id is the host media id.
- Download: `GET /media/{messageId}/{attachmentId}?conversationId=` returns the bytes with `Content-Type` (the stored MIME type) and, when the file has a name, `Content-Disposition: attachment; filename="..."` (`filename*=utf-8''...` for non-ASCII names, RFC 2231). The host checks that the attachment belongs to the login; it does not check `messageId` or `conversationId`. Another login's media is `404 attachment_not_found`.
- Uploads are limited to 100 MiB.

## 10. Fake network (tests only)

With `BRIDGE_HOST_FAKE=true` the host adds network `fake` (flow `phone`, section 4, and flow `emoji`: a `display_and_wait` emoji step 🐱 that completes 30 s later; `BRIDGE_HOST_FAKE_ID` serves it under another id, for example when a client already uses the id `fake` for its own test network; login keys then start with that id) and these control endpoints behind the same bearer token. They do not exist otherwise.

| Endpoint | Body | Effect |
|---|---|---|
| `POST /fake/inject` | `{ loginId, kind: message or edit or delete or reaction or unreaction or read or state, conversationId, senderId? (default alice), messageId, text?, emoji?, state?, attachment?: { fileName, mimeType, dataBase64 } }` | Queues a remote event through bridgev2. 204. A conversation id `dm-<user>` is a DM with `<user>`; any other id is a group with `alice` and `bob`. |
| `POST /fake/seed` | `{ loginId }` | Creates group `team` (10 messages, `seed-team-05` has a 1x1 PNG) and DM `dm-alice` (3 messages), all in the past, and waits until they are bridged. Fixed ids, so a repeat is a no-op. Returns `{ sendTarget: { conversationId }, media: { conversationId, messageId, attachmentId } }`. |
| `POST /fake/revoke` | `{ loginId }` | The network ends the session: bridge state `BAD_CREDENTIALS`. Remote events injected afterwards are held and delivered when the next login of the account connects. 204. |
| `POST /fake/redeliver` | `{ loginId }` | Appends a copy of the newest `message_upsert` frame with a new seq (an at-least-once duplicate). `{ seq }`, or 404 when there is none. |

## 11. Environment

`BRIDGE_HOST_TOKEN` (required), `BRIDGE_HOST_ADDR`, `BRIDGE_HOST_DATA_DIR`, `BRIDGE_HOST_NETWORKS`, `BRIDGE_HOST_FAKE`, `BRIDGE_HOST_FAKE_ID`, `BRIDGE_HOST_LOG_LEVEL`, `TELEGRAM_API_ID`, `TELEGRAM_API_HASH`: see README.md. No other network needs an env variable (docs/networks.md).

## 12. Tests

```
go test -race ./...
```

The tests run the host in process with the fake network (section 10). They cover the login steps, the event stream with cursors, the idempotent send, media, logout, data deletion, a session that the network ends, and the isolation of two users. No test uses a real network account.
