# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- macula-cli builds on macula-go v0.9.0.
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
