# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
