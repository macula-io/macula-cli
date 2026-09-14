# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
