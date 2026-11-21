# Notice

`roomcord-bridge-host` is free software under the GNU Affero General Public License, version 3 or later (see [LICENSE](LICENSE)).

The compiled binary statically links the third-party Go modules below. The combined binary is distributed under AGPL-3.0, because it links AGPL-3.0 code: mautrix-whatsapp, mautrix-telegram, mautrix-meta, mautrix-slack, mautrix-linkedin, mautrix-twitter, mautrix-gmessages, beeper/groupme and beeper/tumblr-dms, and with `-tags signal` also mautrix-signal and the Rust library libsignal. The binary also contains GPL-3.0 code: `go.mau.fi/libsignal` and `github.com/beeper/groupme-lib`. GPL-3.0 section 13 and AGPL-3.0 section 13 permit this combination.

The module list is the `dep` set of `go version -m` on the default build. Each licence was read from the licence file of that module version in the Go module cache. "AGPL-3.0-or-later" means: the LICENSE file is the AGPL-3.0 text and the source headers say "version 3 of the License, or (at your option) any later version".

Two modules have no licence file:

- `github.com/beeper/poly1305`: the source headers state a BSD-style licence (fork of `golang.org/x/crypto`).
- `github.com/beeper/push-receiver`: the README and the source headers state MIT.

Several bridges publish a `LICENSE.exceptions` file. It grants extra rights to Beeper and Element only. Those exceptions do not apply to this project and the file is not copied here.

## Bridges and main libraries

| Module | Version | Licence | File | Note |
|---|---|---|---|---|
| `github.com/beeper/groupme` | `v0.0.0-20261120193234-1bbba535abb9` | AGPL-3.0-or-later | LICENSE |  |
| `github.com/beeper/groupme-lib` | `v0.2.1-0.20261113165139-6773c3ac00ad` | GPL-3.0 | LICENSE | Linked through beeper/groupme. |
| `github.com/beeper/line` | `v0.0.0-20261120193203-6b8d57b4e113` | MIT | LICENSE |  |
| `github.com/beeper/tumblr-dms` | `v0.1.1-0.20261120193536-78a2515b6853` | AGPL-3.0-or-later | LICENSE | The module also has a `LICENSE.exceptions` file. It grants extra rights to Beeper and Element only. This project does not use them. |
| `github.com/coder/websocket` | `v1.8.15` | ISC | LICENSE.txt |  |
| `github.com/gotd/ige` | `v0.2.2` | MIT | LICENSE |  |
| `github.com/gotd/neo` | `v0.1.5` | BSD-3-Clause | LICENSE |  |
| `github.com/mattn/go-sqlite3` | `v1.14.53` | MIT | LICENSE | SQLite driver (cgo). |
| `go.mau.fi/libsignal` | `v0.2.3` | GPL-3.0 | LICENSE | A Go implementation of the Signal protocol. Linked through whatsmeow. |
| `go.mau.fi/mautrix-gmessages` | `v0.2611.1-0.20261119194137-ed5dc19eb2b4` | AGPL-3.0-or-later | LICENSE | The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/mautrix-linkedin` | `v0.2611.1-0.20261120174201-6a016fbdf6e3` | AGPL-3.0-or-later | LICENSE | The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/mautrix-meta` | `v0.2611.1-0.20261120174120-721148544bfb` | AGPL-3.0-or-later | LICENSE | Messenger (`pkg/connector`) and Instagram (`pkg/igconnector`). The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/mautrix-slack` | `v0.2611.1-0.20261120192952-6553d937cc8d` | AGPL-3.0-or-later | LICENSE | The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/mautrix-telegram` | `v0.2611.1-0.20261120192921-8c20292b3b29` | AGPL-3.0-or-later | LICENSE | Contains a gotd fork under `pkg/gotd` with its own MIT LICENSE. The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/mautrix-twitter` | `v0.2609.1-0.20261120174138-77b2290c8537` | AGPL-3.0-or-later | LICENSE | The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/mautrix-whatsapp` | `v0.2611.1-0.20261120223127-eecc2538bf7e` | AGPL-3.0-or-later | LICENSE | The module also has a `LICENSE.exceptions` file for Beeper and Element only. This project does not use it. |
| `go.mau.fi/util` | `v0.11.1` | MPL-2.0 | LICENSE |  |
| `go.mau.fi/whatsmeow` | `v0.0.0-20261116172437-19d61352436f` | MPL-2.0 | LICENSE | WhatsApp client library, used by mautrix-whatsapp. |
| `maunium.net/go/mautrix` | `v0.33.1-0.20261120111454-33abe66d85a0` | MPL-2.0 | LICENSE | Provides `bridgev2`. |

## Other linked modules

| Module | Version | Licence | File | Note |
|---|---|---|---|---|
| `filippo.io/edwards25519` | `v1.2.0` | BSD-3-Clause | LICENSE |  |
| `git.min.rip/min/webpush-client-go` | `v1.3.0` | MIT | LICENSE |  |
| `github.com/PuerkitoBio/goquery` | `v1.13.0` | BSD-3-Clause | LICENSE |  |
| `github.com/andybalholm/brotli` | `v1.2.4` | MIT | LICENSE |  |
| `github.com/andybalholm/cascadia` | `v1.3.4` | BSD-2-Clause | LICENSE |  |
| `github.com/apache/thrift` | `v0.25.0` | Apache-2.0 | LICENSE | The LICENSE file also lists BSD-style terms for some bundled files. |
| `github.com/armfazh/h2c-go-ref` | `v0.0.0-20210215173008-07e12a6f8e0d` | BSD-3-Clause | LICENSE |  |
| `github.com/armfazh/tozan-ecc` | `v0.1.3` | BSD-3-Clause | LICENSE |  |
| `github.com/aws/aws-sdk-go` | `v1.38.20` | Apache-2.0 | LICENSE.txt |  |
| `github.com/beeper/argo-go` | `v1.1.2` | MIT | LICENSE |  |
| `github.com/beeper/poly1305` | `v0.0.0-20250815183548-d4eede7bbf3c` | BSD-3-Clause (from source headers) | none | No LICENSE file in the module. The source headers say "Copyright The Go Authors ... BSD-style license". It is a fork of `golang.org/x/crypto`. |
| `github.com/beeper/push-receiver` | `v0.0.0-20260611104659-b7d6a21272e0` | MIT (from README and source headers) | README.md | No LICENSE file in the module. The README says "MIT License" and the source headers say "go-push-receiver, which is MIT licensed". Proto files: The Chromium Authors, BSD-style. Linked through tumblr-dms. |
| `github.com/bwesterb/go-ristretto` | `v1.2.4` | MIT | LICENSE |  |
| `github.com/bytemare/cryptotools` | `v0.0.0-20210309005813-f9985b05742f` | MIT | LICENSE |  |
| `github.com/bytemare/opaque` | `v0.0.0-20210310201842-16a10d180656` | MIT | LICENSE |  |
| `github.com/bytemare/voprf` | `v0.0.0-20210221170502-f0af88d24d16` | MIT | LICENSE |  |
| `github.com/cenkalti/backoff/v4` | `v4.3.0` | MIT | LICENSE |  |
| `github.com/cespare/xxhash/v2` | `v2.3.0` | MIT | LICENSE.txt |  |
| `github.com/coreos/go-systemd/v22` | `v22.7.0` | Apache-2.0 | LICENSE |  |
| `github.com/disintegration/imaging` | `v1.6.2` | MIT | LICENSE |  |
| `github.com/elliotchance/orderedmap/v3` | `v3.1.0` | MIT | LICENSE |  |
| `github.com/fxamacker/cbor/v2` | `v2.9.4` | MIT | LICENSE |  |
| `github.com/gabriel-vasile/mimetype` | `v1.4.16` | MIT | LICENSE |  |
| `github.com/go-faster/errors` | `v0.7.1` | BSD-3-Clause | LICENSE |  |
| `github.com/go-faster/jx` | `v1.2.0` | MIT | LICENSE |  |
| `github.com/go-faster/xor` | `v1.0.0` | BSD-3-Clause | LICENSE |  |
| `github.com/google/flatbuffers` | `v25.12.19+incompatible` | Apache-2.0 | LICENSE |  |
| `github.com/google/go-querystring` | `v1.2.0` | BSD-3-Clause | LICENSE |  |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | LICENSE |  |
| `github.com/gorilla/websocket` | `v1.5.0` | BSD-2-Clause | LICENSE |  |
| `github.com/gtank/ristretto255` | `v0.2.0` | BSD-3-Clause | LICENSE |  |
| `github.com/icholy/digest` | `v1.2.0` | MIT | LICENSE |  |
| `github.com/imroc/req/v3` | `v3.56.0` | MIT | LICENSE | Replaced in go.mod by the fork `github.com/beeper/req/v3 v3.0.0-20261112020631-42bebc180e13` (MIT, read from the fork). |
| `github.com/jmespath/go-jmespath` | `v0.4.0` | Apache-2.0 | LICENSE |  |
| `github.com/klauspost/compress` | `v1.20.4` | Apache-2.0, BSD-3-Clause, MIT | LICENSE | The LICENSE file states all three for different parts. |
| `github.com/lib/pq` | `v1.12.3` | MIT | LICENSE |  |
| `github.com/mattn/go-colorable` | `v0.1.15` | MIT | LICENSE |  |
| `github.com/mattn/go-isatty` | `v0.0.20` | MIT | LICENSE |  |
| `github.com/openziti/secretstream` | `v0.1.53` | MIT | LICENSE |  |
| `github.com/petermattis/goid` | `v0.0.0-20261109124353-7cfa343ad0ab` | Apache-2.0 | LICENSE |  |
| `github.com/pkg/errors` | `v0.9.1` | BSD-2-Clause | LICENSE |  |
| `github.com/quic-go/qpack` | `v0.6.0` | MIT | LICENSE.md |  |
| `github.com/quic-go/quic-go` | `v0.64.1` | MIT | LICENSE |  |
| `github.com/refraction-networking/utls` | `v1.8.2` | BSD-3-Clause | LICENSE |  |
| `github.com/rs/xid` | `v1.6.0` | MIT | LICENSE |  |
| `github.com/rs/zerolog` | `v1.35.1` | MIT | LICENSE |  |
| `github.com/segmentio/asm` | `v1.2.1` | MIT | LICENSE |  |
| `github.com/skip2/go-qrcode` | `v0.0.0-20200617195104-da1b6568686e` | MIT | LICENSE |  |
| `github.com/slack-go/slack` | `v0.24.0` | BSD-2-Clause | LICENSE | Replaced in go.mod by the fork `github.com/beeper/slackgo v0.0.0-20261117191942-d0ae74c35f48`. The licence is read from the fork. |
| `github.com/tidwall/gjson` | `v1.20.0` | MIT | LICENSE |  |
| `github.com/tidwall/match` | `v1.2.0` | MIT | LICENSE |  |
| `github.com/tidwall/pretty` | `v1.3.0` | MIT | LICENSE |  |
| `github.com/tidwall/sjson` | `v1.2.5` | MIT | LICENSE |  |
| `github.com/u2takey/ffmpeg-go` | `v0.5.0` | Apache-2.0 | LICENSE |  |
| `github.com/u2takey/go-utils` | `v0.3.1` | Apache-2.0 | LICENSE |  |
| `github.com/vektah/gqlparser/v2` | `v2.5.27` | MIT | LICENSE |  |
| `github.com/vmihailenco/msgpack/v5` | `v5.2.0` | BSD-2-Clause | LICENSE |  |
| `github.com/vmihailenco/tagparser/v2` | `v2.0.0` | BSD-2-Clause | LICENSE |  |
| `github.com/x448/float16` | `v0.8.4` | MIT | LICENSE |  |
| `github.com/yuin/goldmark` | `v1.8.7` | MIT | LICENSE |  |
| `go.mau.fi/webp` | `v0.3.0` | BSD-3-Clause | LICENSE |  |
| `go.mau.fi/zeroconfig` | `v0.2.0` | MPL-2.0 | LICENSE |  |
| `go.mau.fi/zerozap` | `v0.1.2` | MPL-2.0 | LICENSE |  |
| `go.opentelemetry.io/otel` | `v1.47.0` | Apache-2.0 | LICENSE | The LICENSE file also has a BSD-3-Clause part for code from the Go authors. |
| `go.opentelemetry.io/otel/trace` | `v1.47.0` | Apache-2.0 | LICENSE | The LICENSE file also has a BSD-3-Clause part for code from the Go authors. |
| `go.uber.org/atomic` | `v1.12.0` | MIT | LICENSE.txt |  |
| `go.uber.org/multierr` | `v1.11.0` | MIT | LICENSE.txt |  |
| `go.uber.org/zap` | `v1.28.0` | MIT | LICENSE |  |
| `golang.org/x/crypto` | `v0.59.0` | BSD-3-Clause | LICENSE |  |
| `golang.org/x/exp` | `v0.0.0-20261113113407-e3970619a1a4` | BSD-3-Clause | LICENSE |  |
| `golang.org/x/image` | `v0.48.0` | BSD-3-Clause | LICENSE |  |
| `golang.org/x/net` | `v0.62.0` | BSD-3-Clause | LICENSE |  |
| `golang.org/x/sync` | `v0.25.0` | BSD-3-Clause | LICENSE |  |
| `golang.org/x/sys` | `v0.50.0` | BSD-3-Clause | LICENSE |  |
| `golang.org/x/text` | `v0.44.0` | BSD-3-Clause | LICENSE |  |
| `google.golang.org/protobuf` | `v1.36.12` | BSD-3-Clause | LICENSE |  |
| `gopkg.in/natefinch/lumberjack.v2` | `v2.2.1` | MIT | LICENSE |  |
| `gopkg.in/yaml.v3` | `v3.0.1` | Apache-2.0 and MIT | LICENSE | The LICENSE file states both. |
| `rsc.io/qr` | `v0.2.0` | BSD-3-Clause | LICENSE |  |

The Go standard library (BSD-3-Clause) is part of every Go binary. `libolm` is not linked: the host does not import the Matrix crypto packages.

## Signal build (`-tags signal`)

`make build-signal` adds these modules and statically links the Rust library `libsignal_ffi.a`:

| Component | Version | Licence | File |
|---|---|---|---|
| `go.mau.fi/mautrix-signal` | `v0.2611.1-0.20261118112413-11ede28800d5` | AGPL-3.0-or-later | LICENSE |
| `libsignal` (`libsignal-ffi` crate, github.com/signalapp/libsignal) | `v0.106.1`, commit 904c8dc8216073b078a5f2f5090e3da8382e6a92 | AGPL-3.0 | LICENSE |
| `github.com/emersion/go-vcard` | `v0.0.0-20241024213814-c9703dde27ff` | MIT | LICENSE |
| `github.com/mattn/go-pointer` | `v0.0.1` | MIT | LICENSE |
| `google.golang.org/genproto/googleapis/rpc` | `v0.0.0-20260817212433-ac3dfec99bb1` | Apache-2.0 | LICENSE |
| `google.golang.org/grpc` | `v1.85.0` | Apache-2.0 | LICENSE |

Not listed: the Rust crates that `libsignal_ffi.a` links (BoringSSL and the crates.io dependencies of libsignal). Their licences are not checked here. `cargo deny list` in the libsignal checkout lists them.
