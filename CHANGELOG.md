# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.12.0] - 2026-10-06

One person joins a realm once, and each of their clients can call
realm-gated procedures with a short note from them
([macula-architecture#15](https://github.com/macula-io/macula-architecture/issues/15)).

### Added

- `person init`: a person key (`person.key` beside the node key, or
  `-person`). It signs, and never connects.
- `person join -realm <name>`: the realm's join session for the person key;
  confirm at the join URL signed in as yourself. The membership UCAN the realm
  issues to that key is kept beside it (`person/<person id>/<realm>.ucan`, owner-only, written atomically),
  after checking it is the person's.
- `person delegate -realm <name> -realm-key ... -to <client node_id>`: a note
  (a UCAN from the person to the client, its parent the membership, granting
  what the membership grants) for `-ttl` (default 24h, at most 168h, never
  past the membership's expiry), written as a chain file (`-out`): the note,
  then the membership. It refuses a membership that is not the person's, not
  signed by that realm key, or expired, and checks the note as a gate would
  before handing it out. A note is revoked only by its expiry.
- `call -ucan-file <file>`: presents a chain (the token, then its proofs, a
  line each) to a gated procedure.

### Changed

- macula-cli runs on macula-go v0.24.0, whose UCAN minting refuses an expiry
  beyond ten years or a window that never opens.

## [0.11.0] - 2026-10-05

Every link now settles on SecP384r1MLKEM1024, the one hybrid key exchange
group that meets both CNSA 2.0 (ML-KEM-1024 with P-384) and BSI TR-02102
(hybrid only). Before, macula-cli landed on SecP256r1MLKEM768 with every
station ([#4](https://github.com/macula-io/macula-cli/issues/4)).

### Changed

- macula-cli runs on macula-go v0.23.0, whose dial offers SecP384r1MLKEM1024
  alone and refuses a handshake that settled on any other group. Go's
  crypto/tls ignores the order of its group list and offered SecP256r1MLKEM768
  first, and a station's rustls takes the client's first group, so v0.20.0's
  two-group list always negotiated SecP256r1MLKEM768. A station that accepts
  only SecP256r1MLKEM768 now fails in the handshake; every macula 12 station
  accepts SecP384r1MLKEM1024.

## [0.10.1] - 2026-10-05

### Fixed

- `pubsub watch` writes its `watching <topic> as node <id>` line to stderr
  under `-json` too (stdout still carries only envelopes), so a script can
  publish once the subscription stands. `scripts/live_check.sh` now publishes
  on that line instead of after a fixed 8 s, which lost the round whenever
  keygen plus connect took longer (#3).

## [0.10.0] - 2026-09-29

### Added

- macula-cli runs on macula-go v0.20.0, the macula 13 wire: every link dials
  handshake v5 and falls back to v4 only for a station never seen on v5 (a
  station seen on v5 is never accepted on v4, macula#53).
- `call` seals end to end when the provider's advertisement names a KEM key,
  and reports it: `sealed` (1 or 0), `provider` and `seal_key_id` in the
  `-json` result, and a line on stderr in text mode.
- `serve -confidential preferred|required|off` (default `preferred`): the node
  advertises a KEM key and answers sealed calls sealed; `required` also refuses
  clear calls; `off` names no key and serves in the clear. Each call it answers
  reports `sealed`.

## [0.9.0] - 2026-09-26

### Breaking

- macula-cli speaks the macula 12 wire (macula-go v0.16.0) and nothing older:
  post-quantum identities and key exchange, signed requests, seeds pinned by
  node_id. 0.8.0 and earlier cannot reach the current fleet.
- Every mesh command takes `-seed host[:port]@<station node_id>` (repeatable)
  instead of a positional host, and `-realm <name or id>` with
  `-realm-key <hex|@file>`.
- The node key is `identity.key`, a macula 12 key (`pq_hybrid` by default,
  `-profile pq_pure`). The 10.x `identity.seed` is not read; a file holding one
  is refused, naming it.
- Removed: the daemon (`daemon start|status|stop`, `serve -daemon`,
  `call -via-daemon`, `pubsub subscribe|unsubscribe`, `watch -daemon`),
  `ucan mint|inspect` (the Ed25519 UCAN nothing in macula 12 consumes),
  `identity sign` (the v1 proof realm 12 refuses, macula-cli#1), `content put`
  (now `content share`), and the flags `-direct`, `-realm-ca`, `-org`, `-ucan`.
- `-json` failures carry `kind` (and a wire `code` and `detail`) instead of
  BOLT#4 fields: `provider_error`, `relay_error`, `stream_error`,
  `identity_mismatch`, `realm_refusal`, `timeout`, `no_provider`,
  `no_realm_key`, `not_found`, `not_shared`, `content_unavailable`,
  `invalid_argument`, `failed`. A malformed invocation under `-json` is an
  `invalid_argument` envelope with exit 2; `-h` exits 0.
- `serve` reports the calls it answered, and `withdrawn` 0 with
  `withdraw_error` when withdrawing the procedure failed.

### Added

- `realm join`, `realm status`, `realm membership`: joining a realm and asking
  for the membership UCAN, each request signed with realm proof v2
  (macula-go's `devicerequest`, macula-realm#29). Fixes macula-cli#1.
- `identity prove-ownership`: a payload with an ownership proof v2
  (`asserted_by`, mcl-om#7), signed with macula-go's `ownershipproof`, which
  mcl_om 0.32 verifies.
- `-ephemeral`: a key made for the run and never saved.
- A procedure `~/<name>` is `<name>` in the node's own namespace.
- `content share` serves node-served content (macula 12's D27) while it runs.
- Tests for every command's core against macula-go's in-process teststation,
  and `scripts/live_check.sh` for one check against a fleet station.

## [0.8.0] - 2026-09-15

### Breaking

- A procedure served with `-exec` or `-echo` whose call payload is a map
  sees the caller the daemon's session verified under `"caller"`, as `0x`
  hex, in place of any `"caller"` the sender put there (macula-go v0.10.0).

### Security

This release fixes these defects in 0.7.1 and earlier releases, through
macula-go v0.10.0.

- A content fetch by MCID used a fetched manifest before it was checked
  against the MCID asked for, and took its size, chunk count and chunk size
  as given, which could stop or stall the process.
- A RESULT, ERROR or STREAM_REPLY counted without a check that it was signed
  by the key its `responded_by` or `reported_by` names.
- A dedicated stream reached a provider without a check that its STREAM_OPEN
  was signed by its caller.
- CBOR decoding had no limit on how deeply lists and maps nest.

### Changed

- macula-go v0.7.1 to v0.10.0.
- `pubsub watch` without `-daemon` exits with an error when it falls behind
  its 256-event queue, and a daemon subscription that falls behind is
  replaced (macula-go v0.9.0).
- Inbound CALLs to a procedure `serve` answers wait in a queue of 64, and a
  CALL that doesn't fit is answered with `temporary_relay_failure`
  (macula-go v0.9.0).
- The daemon holds one connection to its station, under its own identity,
  for serving, outgoing calls and subscriptions. Outgoing calls and
  subscriptions are made as the daemon's own identity. `daemon status`
  reports that one connection: `connected` says whether it is up and
  `last_error` why it was last lost.
- Calls through the daemon run concurrently.
- After the daemon's connection is lost and restored, its procedures are
  advertised and its topics subscribed again together.

### Fixed

- A daemon subscription to a topic with a `*` segment receives the events it
  matches.
- A daemon subscription whose SUBSCRIBE is slow to write no longer holds up
  unsubscribes, status reports and other subscriptions while it waits.
