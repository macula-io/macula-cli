# How to use macula-cli

Every command, every flag, and the patterns for scripting against it. Flags
come before positional arguments (Go's `flag` package stops at the first
positional one).

## 1. Install, identity and the flags every mesh command takes

Install with `install.sh` / `install.ps1` (see the README). The first command
that needs a key creates `identity.key` in the user config directory's
`macula-cli/` (`$XDG_CONFIG_HOME` or `~/.config` on Linux, `~/Library/Application
Support` on macOS, `%AppData%` on Windows). A new key solves the admission
puzzle, which takes a few seconds; the file is readable by its owner only.

```bash
macula-cli identity
# node_id 00a1...
# key_id  7c3e...
# profile pq_hybrid (public key 3118 bytes)
```

| Flag | Meaning |
|------|---------|
| `-seed host[:port]@<node_id hex>` | a station to link to, pinned by the node_id it must prove; repeatable; port 4433 when absent; an IPv6 host is bracketed |
| `-realm <name or id>` | the realm: its name (`io.macula`) or 64-hex id, the name's sha256 |
| `-realm-key <hex or @file>` | the realm key as carried; needed for `<org>/<name>` procedures, not for a node's own namespace |
| `-identity <file>` | the node key file (default above) |
| `-profile pq_hybrid\|pq_pure` | the crypto profile; the fleet's is `pq_hybrid` |
| `-ephemeral` | a key made for this run and never saved |
| `-timeout <duration>` | how long to wait for the first link, and for each call (30s) |
| `-json` | one JSON envelope on stdout |

Quote a procedure that starts with `~`: `'~/echo'` is `echo` in this node's
own namespace, and `'~<node_id>/echo'` names it in another node's. Unquoted,
the shell turns `~/echo` into a path under your home directory.

A 10.x `identity.seed` is not a macula 12 key: pointing `-identity` at one is
refused, naming the file. Move it aside and a new key is created.

## 2. `connect`

```bash
macula-cli connect -seed 'station-fi-helsinki.macula.io:4433@004d1f47...'
# resolved [2a01:4f9:...] in 12 ms
# linked as node 00a1... to station 004d1f47... in 480 ms (up: true)
```

Resolves the seed's host, then links over the macula 12 handshake (QUIC with
ML-KEM hybrid key exchange, then the v4 handshake). A station that does not
prove the pinned node_id is refused.

## 3. `call`

```bash
macula-cli call -seed "$SEED" -realm io.macula -realm-key @io.macula.key -payload '"hello"' mcl-echo/echo
# "hello"
```

| Flag | Meaning |
|------|---------|
| `-payload <json>` | the payload (default `null`) |
| `-payload-file <file>` | read it from a file |
| `-provider <node_id>` | call this provider; any trusted one when absent |

The call reaches a provider by direct dial: its advertisement is read from
the DHT, trusted only when the realm key authorizes it (or, in a node's own
namespace, when that node signed it), and the station it serves from is
dialed pinned.

## 4. `serve`

```bash
macula-cli serve -seed "$SEED" -realm io.macula '~/echo'        # own namespace: no org, no realm key
macula-cli serve -seed "$SEED" -realm io.macula -realm-key @k -reply '{"ok": 1}' acme/status
```

| Flag | Meaning |
|------|---------|
| `-reply <json>` | answer every call with this; echo the caller's payload when absent |
| `-once` | exit after answering one call |
| `-for <duration>` | stop after this long; default until interrupted |

Each call prints `call from <caller node_id>: <payload>`. An `<org>/<name>`
procedure is served only when the org has delegated it to this node in the
DHT; the pool refuses to advertise without it.

## 5. `pubsub`

```bash
macula-cli pubsub watch -seed "$SEED" -realm io.macula -count 1 -for 1m mcl-cli/demo/greeting_sent_v1
macula-cli pubsub publish -seed "$SEED" -realm io.macula -payload '{"id": 7}' mcl-cli/demo/greeting_sent_v1
```

`watch` prints each verified event once, however many links deliver it
(`-count`, `-for`); with `-json`, one envelope per event. `publish` takes
`-payload`, `-payload-file` and `-ttl`. Name topics after a kind of fact and
put ids in the payload.

## 6. `stream probe`

```bash
macula-cli stream probe -seed "$SEED_A" -seed "$SEED_B" -realm io.macula -chunks 8 -size 1024
# 8 chunks, 8192 bytes, round trip in 210 ms
```

Two keys made for the run: a provider linked to the first seed serves a
bidirectional echo in its own namespace, and a caller linked to the last seed
opens it, sends each chunk and checks it comes back unchanged.

## 7. `content`

```bash
macula-cli content share -seed "$SEED" -realm io.macula notes.pdf       # prints the content id, serves until stopped
macula-cli content get -seed "$SEED" -realm io.macula -out notes.pdf <mcid>
macula-cli content probe -seed "$SEED_A" -seed "$SEED_B" -realm io.macula -size 300000
```

Content is node-served: `share` keeps it in this node, serves it and announces
it in the realm while it runs; stop it and it is gone. `get` finds the nodes
that announce the content id, fetches from them and checks every block against
it, so it trusts no sharer (`-max-bytes` bounds it). A content id is 100 hex
characters: SHA-384, codec 0x55 for one block, 0x56 for a manifest over
256 KiB chunks.

## 8. `dht`

```bash
macula-cli dht find-records-by-type -seed "$SEED" station_endpoint
macula-cli dht find-record -seed "$SEED" <storage key hex>
macula-cli dht find-records -seed "$SEED" <storage key hex>
```

Read-only. Every record is verified before it is printed; `dropped` counts
the ones that failed. Types by name: `node_record`, `procedure_advertisement`,
`tombstone`, `content_announcement`, `station_endpoint`, `org_directory`,
`procedure_delegation`, or a number.

## 9. `realm`

Joining a realm has a person in the middle, on purpose.

```bash
macula-cli realm join -realm io.macula
# join session 0193..., pending until ...
# admit the device at https://realm.macula.io/join/0193...
macula-cli realm status 0193...
# status confirmed
# org mri:org:io.macula/...
# citizen_did ...
```

`realm join` sends the carried public key and `device_info` (hostname, OS,
this version; `-hostname` overrides) to `-realm-url`
(`https://realm.macula.io`), signed with realm proof v2: the key signs the
whole request, the realm, the procedure, a timestamp and a nonce, so what the
admitter reads is what the device sent. `-wait <duration>` polls until the
session is confirmed. macula-cli never admits anything.

```bash
macula-cli realm membership -seed "$SEED" -realm io.macula -realm-key @io.macula.key
```

Asks the realm over the mesh for this node's membership UCAN, with the same
proof. The node must have been admitted; `-realm` is the realm's name here,
since the procedure is named under it.

## 10. `identity prove-ownership`

```bash
macula-cli identity prove-ownership -realm io.macula -procedure mcl-graph/learn_link \
  -payload '{"subject": "entity:alpha", "object": "entity:beta"}'
# {"object":"entity:beta","subject":"entity:alpha","asserted_by":{"identity":"00a1...","proof":{...}}}
```

Prints the payload with an ownership proof v2 (mcl-om#7): this node's key
signs every field, the procedure, the realm, a timestamp and a nonce, so a
service accepts it once, for that procedure, within 60 s, and refuses it with
any field changed. Send the printed payload as the call's payload. A payload
carrying `"caller"` is refused: a station replaces that field with the caller
it authenticated.

## 11. Scripting

- Every command takes `-json` and prints one envelope,
  `{"ok": true, "data": ...}` or `{"ok": false, "error": {...}}`.
- `error.kind` is one of `provider_error` (with the provider's `code` and
  `detail`), `relay_error` (`code`), `stream_error` (`code`, `detail`,
  `relay`), `identity_mismatch` (a station that does not prove the pinned
  node_id), `realm_refusal` (the realm's error as `code`, such as
  `bad_proof`, `session_not_found` or `session_expired`, and the HTTP status
  as `detail`), `timeout`, `no_provider`, `no_realm_key`, `not_found`,
  `not_shared`, `content_unavailable`, `invalid_argument` and `failed`. Match
  the kind and code, never the message.
- Exit codes: 0 success (and `-h`), 1 failure, 2 a malformed invocation,
  which under `-json` is an `invalid_argument` envelope too.
- `serve` reports the calls it answered even when withdrawing the procedure
  fails at the end: `withdrawn` is 0 then, with `withdraw_error`.
- Payloads: JSON with no booleans (send 0 and 1), integers within int64,
  bytes as `{"$bytes": "<base64>"}`. Output uses the same forms, so a result
  can be sent back as a payload unchanged.
- A fresh `-ephemeral` key per run keeps a script from depending on, or
  changing, a stored identity.
