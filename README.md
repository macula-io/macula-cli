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
  error) out. `-direct` resolves and dials a station straight from its DHT
  advertisement instead of depending on gossip; `-ucan` attaches a UCAN
  token; `-realm-ca`/`-org` verify a direct-dial advertisement's embedded
  cert chain (Slice 7c Direction B).
- **`serve`** — advertises a procedure, answers one inbound CALL, exits;
  the provider-role counterpart to `call`. Same `-direct`/`-cert-chain`
  flags as `call`, plus `-require-ucan-issuer` to gate the procedure. `
  -daemon` registers it with a running `daemon` instead (see below) —
  persistent, many calls, no exit after the first.
- **`pubsub watch` / `pubsub publish`** — subscribe and stream events as
  newline-delimited JSON, or publish one event and exit.
- **`stream probe`** — opens a Bidi stream across **two different
  stations** and confirms data actually flows both ways through the relay,
  not just that the stream opens.
- **`content probe` / `put` / `get`** — self-contained put+get+verify round
  trip, or upload/download a real file by its MCID.
- **`identity`** — prints this machine's local identity (node ID), purely
  local, no station involved.
- **`ucan mint` / `ucan inspect`** — mint a UCAN token signed by the local
  identity, or decode one's claims without checking its signature.
- **`daemon start` / `status` / `stop`** — macula-cli's optional long-lived
  mode: one process holds a single Session open and answers CALLs for
  whatever `serve -daemon` registers, until stopped. Other `macula-cli`
  invocations control it over a local Unix domain control socket instead
  of each dialing the mesh fresh — see [Daemon mode](#daemon-mode) below.

Every failure is reported through Macula's own
[BOLT#4 error taxonomy](https://github.com/macula-io/macula-go-sdk/blob/master/bolt4/bolt4.go)
(`unknown_next_peer`, `temporary_relay_failure`, etc.) rather than invented
text, so a caller parsing output gets the same failure vocabulary the wire
protocol itself uses.

---

## Status

**Walking skeleton, [v0.1.2](https://github.com/macula-io/macula-cli/releases/tag/v0.1.2)
tagged and released 2026-08-29 (current — matches the tip of `master`).**
All nine commands ran successfully against the real 7-station demo fleet as
each was built — not batched to the end — including finding and fixing
several real bugs along the way (`pubsub watch` crashing on a station
behavior it hadn't accounted for — [HOW-TO guide](guides/HOWTO.md) §4 —
plus a `SIGPIPE`/`pipefail` bug in `install.sh` and a Windows/macOS
identity-path bug in `v0.1.0`, both fixed in `v0.1.1`). `identity`,
`pubsub publish`, and `content put`/`get` were added afterward, driven by
[`macula-mcp`](https://github.com/macula-io/macula-mcp)'s rework onto this
CLI, and shipped in `v0.1.2`. `ucan`/`-direct`/`-cert-chain`/
`-require-ucan-issuer` (on `call`/`serve`) and daemon mode (`daemon`,
`serve -daemon`) followed, matching `macula-go-sdk`'s own direct-dial,
UCAN, cert-chain, and `ServeForever` additions. CI checks
`gofmt`/`vet`/`build` plus a GoReleaser snapshot build, `shellcheck` on the
install/uninstall scripts, and a PowerShell parse-check — no unit tests,
since every command talks to a live station by design; verification is
"run it against the fleet," same convention `macula-go-sdk`'s own
`live`-tagged tests follow.

Unlike this repo, `macula-go-sdk` itself has never been tagged at all;
`go install ...@latest` there resolves to the tip of `master`.

**Not yet built:** anything bridging macula-station's loopback-only admin
API (`/health`, `/wire`, `/dht/stats`, ...) — that needs an SSH tunnel per
station and is deliberately out of scope for now.

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
cmd/macula-cli/          one file per subcommand, single package — argument-parsing
                          glue thin enough that it doesn't earn per-command packages
internal/identitystore/  load-or-mint a puzzle-hardened identity, persisted to disk
internal/report/         one --json envelope / human-text choice, BOLT#4-aware errors
internal/wirevalue/      JSON <-> cbor.Value bridge for --args and output
internal/daemon/         daemon mode: the long-lived Session + registry + control
                          socket "serve -daemon"/"daemon status"/"daemon stop" talk to
```

| Package | Role |
|---|---|
| `cmd/macula-cli` | One file per subcommand (`connect`, `call`, `serve`, `pubsub`, `stream`, `content`, `identity`, `ucan`, `daemon`) and their flag parsing. Thin — every one-shot command is a short, direct sequence of real SDK calls. |
| `internal/identitystore` | Loads a persisted identity or mints a fresh puzzle-hardened one (`identity.Generate` — never the unhardened path). |
| `internal/report` | Shared `--json` / human-text output, surfaces BOLT#4 code/name/retryable for wire-level failures. |
| `internal/wirevalue` | Converts between JSON (what a human or an agent types/reads) and `cbor.Value` (what the wire actually carries) — deliberately narrow, since Macula's CBOR has no `bool` and no float/int ambiguity the way JSON does. |
| `internal/daemon` | `Server` holds one Session and a mutex-guarded procedure registry, driving `macula-go-sdk`'s `ServeForever`; `Do`/`Listen`/`SocketPath` are the newline-delimited-JSON control-socket client and server halves `cmd/macula-cli`'s daemon-aware commands share. |

---

## Daemon mode

Every command above is one-shot: connect, do the one thing, exit. That's
deliberate for `call`/`pubsub publish`/`content put`, but `serve`'s own
one-shot shape means a real long-lived server means wrapping it in your own
shell loop, and there's no way to keep a procedure re-advertised or a
subscription alive without a process staying up the whole time.

**Daemon mode** is the alternative: one `macula-cli daemon start` process
holds a single Session open, and other `macula-cli` invocations control it
over a local Unix domain socket instead of each dialing the mesh fresh —
the same shape as `ssh-agent` or `dockerd`, not a second product.

```bash
# Start the daemon (foreground -- pair with a process supervisor for
# unattended use; Ctrl-C/SIGTERM/"daemon stop" all stop it cleanly).
macula-cli daemon start station-de-frankfurt.macula.io:4433 &

# Register a procedure -- answers as many calls as arrive, not just one.
macula-cli serve -daemon -reply '{"pong":1}' my.echo

# From anywhere else: ordinary "call" reaches it exactly like any other
# advertised procedure -- the daemon is invisible to callers.
macula-cli call station-de-frankfurt.macula.io:4433 my.echo

# Inspect or stop it.
macula-cli daemon status
macula-cli serve -daemon -stop my.echo
macula-cli daemon stop
```

More than one daemon instance can run side by side via `-socket-name`
(e.g. one per identity/realm) — every daemon-aware command takes it, and
`-socket` overrides the derived path outright. The control socket lives
under `$XDG_RUNTIME_DIR/macula-cli` when set (systemd-logind's per-UID
tmpfs, already 0700 and correctly owned before any session starts), or a
UID-scoped `os.TempDir()` directory otherwise — not the user config
directory `identitystore` uses for the identity file, since a Unix domain
socket path is capped at roughly 108 bytes and a config-dir-rooted path
can exceed that depending on `$HOME` (found live, not assumed). On the
`os.TempDir()` fallback, a shared, world-writable temp directory (`/tmp`
on a typical multi-user Linux box) means another local user could
pre-create the target directory before this daemon does, so its
ownership and permissions are verified before trusting it, not just
assumed from `os.MkdirAll` succeeding — refused live against a
deliberately world-writable planted directory during testing.

`serve -daemon`'s registration flags (`-direct`, `-cert-chain`,
`-require-ucan-issuer`, `-reply`/`-echo`) are the same ones the one-shot
`serve` takes; it just sends them to the daemon instead of dialing the
mesh itself and takes no `<host[:port]>` (the daemon already has one).

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
