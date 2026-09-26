# macula-cli

[![CI](https://img.shields.io/github/actions/workflow/status/macula-io/macula-cli/ci.yml?branch=master&label=CI)](https://github.com/macula-io/macula-cli/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0%20OR%20MIT-blue.svg)](#license)
[![Go Reference](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go)](https://go.dev)
[![GitHub Sponsors](https://img.shields.io/badge/GitHub%20Sponsors-support-ea4aaa.svg?logo=githubsponsors&logoColor=white)](https://github.com/sponsors/rgfaber)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/macula-cli-full-dark.svg">
    <img src="assets/macula-cli-full-light.svg" alt="Macula CLI" width="320">
  </picture>
</p>

<p align="center">
  <strong>Test, monitor and use the macula 12 mesh from the command line</strong>
</p>

---

## What is macula-cli?

A scriptable client of the macula 12 mesh: one Go binary, built on
[macula-go](https://github.com/macula-io/macula-go), that links to real
stations and reports exactly what happened. It has no interactive mode by
design. Its consumers are scripts and agents that parse `-json` output, and
every command prints the same data either way.

macula 12 is the post-quantum wire: ML-DSA-87 identities (the ML-DSA-87 +
RSA-PSS-4096 composite in `pq_hybrid`, the fleet's profile), ML-KEM hybrid key
exchange, signed requests, and seeds pinned by the node_id each station must
prove. Releases before 0.9.0 spoke the retired 10.x wire and cannot reach the
current fleet.

## Install

**Linux / macOS:**

```bash
curl -fsSL https://raw.githubusercontent.com/macula-io/macula-cli/master/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/macula-io/macula-cli/master/install.ps1 | iex
```

Both download the release archive for your OS and architecture from
[GitHub Releases](https://github.com/macula-io/macula-cli/releases), check it
against the release's `checksums.txt`, and install `macula-cli`
(`$HOME/.local/bin` on Linux and macOS, `%LOCALAPPDATA%\macula-cli` on Windows;
`MACULA_CLI_INSTALL_DIR` overrides). With Go 1.27:
`go install github.com/macula-io/macula-cli/cmd/macula-cli@latest`.

The public stations have IPv6 addresses only: macula-cli needs a working IPv6
route and outbound UDP to port 4433.

`uninstall.sh` / `uninstall.ps1` (same path) remove the binary and leave the
node key in place; `--purge` / `-Purge` removes it too.

## Quick start

Every station is pinned: `-seed host[:port]@<station node_id hex>`. A realm is
`-realm` (its name, `io.macula`, or its 64-hex id, which is the name's sha256),
trusted with `-realm-key` (the realm key as carried, in hex, or `@file`).

```bash
SEED='station-fi-helsinki.macula.io:4433@004d1f470097ccf8826ce291900e882fdb1f20375e53901facaec0f23eb4efd8'

macula-cli connect -seed "$SEED"
macula-cli dht find-records-by-type -seed "$SEED" node_record
macula-cli call -seed "$SEED" -realm io.macula -realm-key @io.macula.key -payload '"hello"' mcl-echo/echo
```

The first command that needs a key creates one (`identity.key` in the user
config directory's `macula-cli/`; it solves the admission puzzle, which takes
a few seconds). `-ephemeral` uses a key made for the run and never saved;
`-identity <file>` uses another file.

## Commands

| Command | What it does |
|---------|--------------|
| `connect` | Resolve the seed, then link to its station over the macula 12 handshake, refusing a station that does not prove the pinned node_id |
| `call <procedure>` | Call a procedure by direct dial: to any trusted provider, or `-provider <node_id>` |
| `serve <procedure>` | Serve a procedure, echoing each payload or answering `-reply`, until stopped (`-once`, `-for`) |
| `pubsub publish <topic>` | Publish one payload |
| `pubsub watch <topic>` | Print each verified event (`-count`, `-for`) |
| `stream probe` | A bidirectional streaming round trip between two fresh nodes, through their stations |
| `content share <file>` | Serve a file from this node and print its content id, while it runs |
| `content get <mcid>` | Fetch content from the nodes that share it, checked against its id |
| `content probe` | Share and fetch between two fresh nodes |
| `dht find-record`, `find-records <key>` | Verified records under a storage key |
| `dht find-records-by-type <type>` | Verified records of a type: `node_record`, `station_endpoint`, `org_directory`, ... |
| `identity` | This node's key: node_id, key id, profile |
| `identity prove-ownership` | A payload with an ownership proof v2 (`asserted_by`, mcl-om#7) by which this node authorises every field, for one procedure and realm, once |
| `realm join` | Ask a realm to admit this device: a human admits it at the printed join URL (`-wait` polls) |
| `realm status <session>` | A join session's state, and what the realm granted once confirmed |
| `realm membership` | This node's membership UCAN, over the mesh; the node must be admitted |

A procedure `'~/<name>'` is `<name>` in the node's own namespace, which needs no
org and no realm key: `serve '~/echo'` on one node and `call '~<its node_id>/echo'`
on another. An `<org>/<name>` procedure is served only by a node the org has
delegated it to, and called only with the realm's key pinned.

Every request to a realm (`realm join`, `realm membership`) carries a realm
proof v2 (macula-realm#29): the key signs the whole request, the realm, the
procedure, a timestamp and a nonce, so a relay cannot change what the
admitter reads. macula-cli never admits anything: a person does, at the join
URL.

Payloads are JSON on the command line and in the output. macula's wire has no
boolean (send 0 and 1) and carries integers within int64; bytes are
`{"$bytes": "<base64>"}` both ways, so a value received can be sent back
unchanged.

See the [HOW-TO guide](guides/HOWTO.md) for every flag, and for scripting
against `-json`.

## Output and failures

With `-json` every command prints one envelope (`pubsub watch` one per event):

```json
{"ok": true, "data": ...}
{"ok": false, "error": {"kind": "provider_error", "code": "handler_error", "detail": "...", "message": "..."}}
```

`kind` is one of `provider_error`, `relay_error`, `stream_error`,
`identity_mismatch`, `realm_refusal`, `timeout`, `no_provider`, `no_realm_key`, `not_found`,
`not_shared`, `content_unavailable`, `invalid_argument` and `failed`, taken
from macula-go's typed errors and the realm's own error codes, never from a
message. A malformed invocation (a `-json` one included) prints an
`invalid_argument` envelope and exits 2, a failure exits 1, `-h` exits 0.

## Build and test

```bash
go build ./cmd/macula-cli
go test ./...
```

Every command's core is tested against macula-go's in-process teststation (two
macula 12 stations sharing a DHT, and a test realm with one org), and
`realm join` against an HTTP realm that verifies the proof as macula-realm
does. No test touches the network. `scripts/live_check.sh` runs one check
against a fleet station with keys made for the run: connect, node records,
`mcl-echo/echo`, and a watch hearing one publication.

Releases: a `v*` tag builds Linux, macOS and Windows binaries for amd64 and
arm64 with goreleaser and attaches them, with `checksums.txt`, to the GitHub
release.

## Relationship to other repos

- [macula-go](https://github.com/macula-io/macula-go): the macula 12 node this
  CLI drives, and its teststation.
- [macula-station](https://github.com/macula-io/macula-station): the stations
  it links to.
- [macula-realm](https://github.com/macula-io/macula-realm): the realm that
  admits devices and issues membership.

## License

Apache-2.0 OR MIT, at your option. See [LICENSE-APACHE](LICENSE-APACHE) and
[LICENSE-MIT](LICENSE-MIT).
