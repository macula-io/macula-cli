# macula-cli

[![CI](https://img.shields.io/github/actions/workflow/status/macula-io/macula-cli/ci.yml?branch=master&label=CI)](https://github.com/macula-io/macula-cli/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0%20OR%20MIT-blue.svg)](#license)
[![Go Reference](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go)](https://go.dev)
[![Buy Me A Coffee](https://img.shields.io/badge/Buy%20Me%20A%20Coffee-support-yellow.svg)](https://buymeacoffee.com/rlefever)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/macula-cli-full-dark.svg">
    <img src="assets/macula-cli-full-light.svg" alt="Macula CLI" width="320">
  </picture>
</p>

<p align="center">
  <strong>Test, monitor, and diagnose the Macula mesh from the command line</strong>
</p>

---

## What is macula-cli?

A **scriptable client**, not an operator dashboard — one small Go binary,
built directly on [`macula-go-sdk`](https://github.com/macula-io/macula-go-sdk),
that exercises the real wire protocol against a real
[macula-station](https://github.com/macula-io/macula-station) and reports
exactly what happened. No TUI, no interactive mode: the primary consumer is
expected to be a script or an agent shelling out to it and parsing `--json`
output, not a human watching a live view. Every command works the same way
with or without `--json` — human-readable output is a formatting choice, not
a separate code path.

It exists because the SDKs give you clean primitives (connect, call,
publish/subscribe, content, streaming) but no fleet-facing diagnostics of
their own — no way to ask "is this station actually reachable," "did this
stream actually relay across stations," or "did this content round-trip and
Merkle-verify," without writing a throwaway Go program every time. This tool
is that throwaway program, built once and kept.

**What it does, concretely:**

- **`connect`** — stages the handshake (DNS, QUIC/TLS, CONNECT/HELLO)
  separately, so a failure names which stage broke instead of one opaque
  error.
- **`call`** — one unary RPC call, JSON args in, JSON payload (or a BOLT#4
  error) out.
- **`pubsub watch`** — subscribes and streams events as newline-delimited
  JSON, live.
- **`stream probe`** — opens a Bidi stream across **two different
  stations** and confirms data actually flows both ways through the relay,
  not just that the stream opens.
- **`content probe`** — puts random test content, gets it back, confirms
  the bytes and the Merkle verification both check out.

Every failure is reported through Macula's own
[BOLT#4 error taxonomy](https://github.com/macula-io/macula-go-sdk/blob/master/bolt4/bolt4.go)
(`unknown_next_peer`, `temporary_relay_failure`, etc.) rather than invented
text, so a caller parsing output gets the same failure vocabulary the wire
protocol itself uses.

---

## Status

**Walking skeleton, [v0.1.1](https://github.com/macula-io/macula-cli/releases/tag/v0.1.1)
tagged and released 2026-08-29 (current — matches the tip of `master`).**
All five commands ran successfully against the real 7-station demo fleet as
each was built — not batched to the end — including finding and fixing
several real bugs along the way (`pubsub watch` crashing on a station
behavior it hadn't accounted for — [HOW-TO guide](guides/HOWTO.md) §4 —
plus a `SIGPIPE`/`pipefail` bug in `install.sh` and a Windows/macOS
identity-path bug in `v0.1.0`, both fixed in `v0.1.1`). CI checks
`gofmt`/`vet`/`build` plus a GoReleaser snapshot build, `shellcheck` on the
install/uninstall scripts, and a PowerShell parse-check — no unit tests,
since every command talks to a live station by design; verification is
"run it against the fleet," same convention `macula-go-sdk`'s own
`live`-tagged tests follow.

Unlike this repo, `macula-go-sdk` itself has never been tagged at all;
`go install ...@latest` there resolves to the tip of `master`.

**Not yet built:** `content put`/`get` against an arbitrary file or a known
MCID (only `probe`'s self-contained round trip exists today), `pubsub
publish` (only `watch`/subscribe), and anything bridging macula-station's
loopback-only admin API (`/health`, `/wire`, `/dht/stats`, ...) — that needs
an SSH tunnel per station and is deliberately out of scope for now.

---

## Quick start

**Linux / macOS:**

```bash
curl -fsSL https://raw.githubusercontent.com/macula-io/macula-cli/master/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/macula-io/macula-cli/master/install.ps1 | iex
```

Both pull the release archive matching your OS/arch from
[GitHub Releases](https://github.com/macula-io/macula-cli/releases),
verify it against the release's own `checksums.txt`, and install
`macula-cli` (`$HOME/.local/bin` on Linux/macOS, `%LOCALAPPDATA%\macula-cli`
on Windows — override with `MACULA_CLI_INSTALL_DIR`). Prefer building from
source, or already have Go? `go install
github.com/macula-io/macula-cli/cmd/macula-cli@latest` works too.

```bash
macula-cli connect station-de-frankfurt.macula.io
```

To remove it again: `curl -fsSL .../uninstall.sh | bash` (or
`irm .../uninstall.ps1 | iex` on Windows) — same repo path, `uninstall.sh`/
`uninstall.ps1` instead of `install`. Leaves the persisted identity alone
by default (add `--purge`/`-Purge` to remove that too); see the
[HOW-TO guide](guides/HOWTO.md) §1.

**Read the [HOW-TO guide](guides/HOWTO.md) for the full command/flag
reference before scripting against this** — it covers every flag, real
example output for each command, and two gotchas worth knowing up front:
Go's `flag` package requires flags before positional arguments, and
Macula's wire protocol has no `bool` type at all (a JSON boolean in `--args`
is rejected, not silently coerced).

---

## Architecture

```
cmd/macula-cli/          one file per subcommand, single package — a 5-command
                          CLI's argument-parsing glue doesn't earn 5 internal packages
internal/identitystore/  load-or-mint a puzzle-hardened identity, persisted to disk
internal/report/         one --json envelope / human-text choice, BOLT#4-aware errors
internal/wirevalue/      JSON <-> cbor.Value bridge for --args and output
```

| Package | Role |
|---|---|
| `cmd/macula-cli` | The five subcommands (`connect`, `call`, `pubsub`, `stream`, `content`) and their flag parsing. Thin — every command is a short, direct sequence of real SDK calls. |
| `internal/identitystore` | Loads a persisted identity or mints a fresh puzzle-hardened one (`identity.Generate` — never the unhardened path). |
| `internal/report` | Shared `--json` / human-text output, surfaces BOLT#4 code/name/retryable for wire-level failures. |
| `internal/wirevalue` | Converts between JSON (what a human or an agent types/reads) and `cbor.Value` (what the wire actually carries) — deliberately narrow, since Macula's CBOR has no `bool` and no float/int ambiguity the way JSON does. |

---

## Build & test

```bash
go build ./...
go vet ./...
gofmt -l .
```

No unit tests: every command talks to a live station by design. Verify a
change by actually running the affected command against the fleet
(`station-de-frankfurt.macula.io:4433` is the default demo station) — see
the [HOW-TO guide](guides/HOWTO.md) for real example invocations to compare
against.

---

## Documentation

| Guide | Description |
|---|---|
| [HOW-TO Guide](guides/HOWTO.md) | Full command/flag reference, real example output, gotchas found live-testing each command, BOLT#4 error troubleshooting |

---

## Relationship to other repos

| Repo | Role |
|---|---|
| [`macula-io/macula-go-sdk`](https://github.com/macula-io/macula-go-sdk) | The SDK every command in this repo is built directly on — identity, wire protocol, QUIC transport. |
| [`macula-io/macula-station`](https://github.com/macula-io/macula-station) | The relay station this tool connects to and diagnoses. Its own `docs/` incident writeups are useful context for what a failure here might mean station-side. |
| `macula-apps/macula-cam2me` | The real pain that motivated this tool: mesh-connectivity issues discovered building a mobile app with no independent way to test the mesh outside the running app. |

---

## License

Dual-licensed under [Apache-2.0](LICENSE-APACHE) or [MIT](LICENSE-MIT), your choice.

---

<p align="center">
  <sub>Built on macula-go-sdk</sub>
</p>
