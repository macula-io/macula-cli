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
  <strong>Test, monitor, and diagnose the Macula mesh from the command line</strong>
</p>

---

## What is macula-cli?

A **scriptable client**, not an operator dashboard — one small Go binary,
built directly on [`macula-go`](https://github.com/macula-io/macula-go),
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
  cert chain (Slice 7c Direction B); `-via-daemon` routes the call through
  a running `daemon` instead of dialing fresh (not composable with
  `-direct`).
- **`serve`** — advertises a procedure, answers one inbound CALL, exits;
  the provider-role counterpart to `call`. Same `-direct`/`-cert-chain`
  flags as `call`, plus `-require-ucan-issuer` to gate the procedure. `
  -daemon` registers it with a running `daemon` instead (see below) —
  persistent, many calls, no exit after the first. `-exec <command>`
  computes the reply per call (JSON in on stdin, JSON out on stdout)
  instead of a fixed `-reply`/`-echo`.
- **`pubsub watch` / `pubsub publish`** — subscribe and stream events as
  newline-delimited JSON, or publish one event and exit. `watch -daemon`
  taps into a daemon's own subscription instead of subscribing itself;
  `pubsub subscribe`/`unsubscribe` (daemon-only, no non-daemon form)
  start or end a subscription that outlives the command that touched it.
- **`stream probe`** — opens a Bidi stream across **two different
  stations** and confirms data actually flows both ways through the relay,
  not just that the stream opens.
- **`content probe` / `put` / `get`** — self-contained put+get+verify round
  trip, or upload/download a real file by its MCID.
- **`dht find-record` / `find-records` / `find-records-by-type`** — read the
  mesh's signed DHT record store directly: one record by storage key, every
  record at a key (the signer-deduped multiset), or every record of a type
  currently visible from the connecting station. `find-records-by-type
  procedure_advertisement` is the discovery entry point — every capability
  a station knows about, with the realm each is scoped to decoded straight
  out of `procedure_uri` (realm is embedded there, not a separate field; DHT
  storage itself is always the protocol's own all-zero realm, so none of
  the three take a `-realm` flag). Each record's signature is checked and
  reported (`verified`/`verify_error`), never silently assumed good.
- **`identity`** — prints this machine's local identity (node ID), purely
  local, no station involved.
- **`ucan mint` / `ucan inspect`** — mint a UCAN token signed by the local
  identity, or decode one's claims without checking its signature.
- **`daemon start` / `status` / `stop`** — macula-cli's optional long-lived
  mode: one process holds a station connection open (three Sessions,
  actually — see [Daemon mode](#daemon-mode)) and answers CALLs for
  whatever `serve -daemon` registers, until stopped. Other `macula-cli`
  invocations control it over a local Unix domain control socket instead
  of each dialing the mesh fresh — see [Daemon mode](#daemon-mode) below.

Every failure is reported through Macula's own
[BOLT#4 error taxonomy](https://github.com/macula-io/macula-go/blob/master/bolt4/bolt4.go)
(`unknown_next_peer`, `temporary_relay_failure`, etc.) rather than invented
text, so a caller parsing output gets the same failure vocabulary the wire
protocol itself uses.

---

## Status

**Walking skeleton. Last tagged release is
[v0.1.2](https://github.com/macula-io/macula-cli/releases/tag/v0.1.2)
(2026-08-29) — `master` has since moved ahead (UCAN, direct-dial, cert-chain,
and the whole of daemon mode below) and hasn't been re-tagged yet.**
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
`serve -daemon`, then `call -via-daemon` and `pubsub subscribe`/`watch
-daemon`/`unsubscribe`) followed, matching `macula-go`'s own
direct-dial, UCAN, cert-chain, and `ServeForever` additions. The daemon's
three-Session split (see [Daemon mode](#daemon-mode)) was itself a bug fix,
found live: a first draft sharing one Session between serving and
`call -via-daemon` intermittently stole its own reply frames. `dht
find-record`/`find-records`/`find-records-by-type` followed, wrapping
`macula-go`'s existing `dht.FindRecord`/`FindRecords`/`FindRecordsByType`
(itself already complete — this was purely a missing CLI surface). Built to
answer a real question live, not hypothetically: whether a service's
capability (`hecate_stations.list_stations`) that `mesh_call`/`call -direct`
both failed to reach was actually in the DHT at all under any realm.
`find-records-by-type procedure_advertisement` against the demo fleet
answered it directly — 16 real records, all a different service's
(`hecate_mail`, `tube`), none `hecate_stations`'s, confirming the
advertisement genuinely never landed rather than this being a realm- or
routing-side problem. Also surfaced, incidentally, that `hecate_mail`
advertises each of its procedures under two different names (`X.Y` and
`_/X.Y`) — a pre-existing inconsistency in that service's own advertise
code, unrelated to this addition and not fixed here. CI checks
`gofmt`/`vet`/`build` plus a GoReleaser snapshot build, `shellcheck` on the
install/uninstall scripts, and a PowerShell parse-check. Almost no unit
tests, since every command talks to a live station by design and
verification is "run it against the fleet," same convention `macula-go`'s
own `live`-tagged tests follow -- the one deliberate exception is
`internal/daemon/pubsub_test.go`, added alongside the `watchForDisconnect`
fix below: that bug lived entirely in the LOCAL control-socket protocol,
never touched the mesh at all, and is exactly the kind of thing "run it
against the fleet" would never have caught (it didn't, for as long as
daemon mode existed) since it depends on exact byte-level timing a fleet
run can't deterministically reproduce -- a `net.Pipe()`-backed unit test
can, and does, every time.

`macula-go` is now tagged too (`v0.3.0`, current as of the rename below) —
this repo pins an exact tag in `go.mod` rather than a floating pseudo-version,
same as any other dependency.

**2026-08-30: every `macula-{language}-sdk` sibling repo dropped the
redundant "-sdk" suffix** (`macula-go-sdk`→`macula-go`,
`macula-rust-sdk`(-ffi)→`macula-rust`(-ffi), `macula-php-sdk`→`macula-php`,
`macula-dotnet-sdk`→`macula-dotnet`). This repo's own name is unaffected
(it was never `macula-cli-sdk`); only its `go.mod` dependency and doc
links moved to `github.com/macula-io/macula-go`.

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
internal/daemon/         daemon mode: three long-lived Sessions (serve/call/subscribe),
                          the procedure/subscription registries, and the control socket
                          every daemon-aware subcommand talks to
```

| Package | Role |
|---|---|
| `cmd/macula-cli` | One file per subcommand (`connect`, `call`, `serve`, `pubsub`, `stream`, `content`, `identity`, `ucan`, `daemon`) and their flag parsing. Thin — every one-shot command is a short, direct sequence of real SDK calls. |
| `internal/identitystore` | Loads a persisted identity or mints a fresh puzzle-hardened one (`identity.Generate` — never the unhardened path). |
| `internal/report` | Shared `--json` / human-text output, surfaces BOLT#4 code/name/retryable for wire-level failures. |
| `internal/wirevalue` | Converts between JSON (what a human or an agent types/reads) and `cbor.Value` (what the wire actually carries) — deliberately narrow, since Macula's CBOR has no `bool` and no float/int ambiguity the way JSON does. |
| `internal/daemon` | `Server` holds three Sessions (serve/call/subscribe — see [Daemon mode](#daemon-mode)) plus mutex-guarded procedure and subscription registries, driving `macula-go`'s `ServeForever`; `Do`/`Watch`/`Listen`/`SocketPath` are the newline-delimited-JSON control-socket client and server halves `cmd/macula-cli`'s daemon-aware commands share. |

---

## Daemon mode

Every command above is one-shot: connect, do the one thing, exit. That's
deliberate for `call`/`pubsub publish`/`content put`, but `serve`'s own
one-shot shape means a real long-lived server means wrapping it in your own
shell loop, and there's no way to keep a procedure re-advertised or a
subscription alive without a process staying up the whole time.

**Daemon mode** is the alternative: one `macula-cli daemon start` process
holds a station connection open, and other `macula-cli` invocations control
it over a local Unix domain socket instead of each dialing the mesh fresh —
the same shape as `ssh-agent` or `dockerd`, not a second product.

```bash
# Start the daemon (foreground -- pair with a process supervisor for
# unattended use; Ctrl-C/SIGTERM/"daemon stop" all stop it cleanly).
macula-cli daemon start station-de-frankfurt.macula.io:4433 &

# Register a procedure -- answers as many calls as arrive, not just one.
macula-cli serve -daemon -reply '{"pong":1}' my.echo

# Or -exec: a real, per-call computed reply instead of a fixed one --
# the script gets the call's JSON payload on stdin, its stdout becomes
# the reply. A non-zero exit, a timeout, or invalid JSON on stdout all
# become a normal ERROR reply to the caller, not a crash of the daemon
# or any other procedure it's serving.
macula-cli serve -daemon -exec './double.sh' my.double

# From anywhere else: ordinary "call" reaches it exactly like any other
# advertised procedure -- the daemon is invisible to callers. Or route
# through the daemon's own connection instead of dialing fresh:
macula-cli call station-de-frankfurt.macula.io:4433 my.echo
macula-cli call -via-daemon my.echo

# A subscription outlives the command that created it -- "watch" taps in
# and out freely without ending it.
macula-cli pubsub subscribe my.topic
macula-cli pubsub watch -daemon my.topic &
macula-cli pubsub unsubscribe my.topic

# Inspect or stop it.
macula-cli daemon status
macula-cli serve -daemon -stop my.echo
macula-cli daemon stop
```

**Three Sessions, not one.** A daemon connects three times, not once: one
Session (the daemon's real, persisted identity) owns serving and every
advertisement; a second, ephemeral-identity Session is dedicated to
`call -via-daemon`; a third, separately ephemeral, is dedicated to every
`pubsub subscribe`d topic, sharing one receive loop that dispatches by
topic. This isn't caution for its own sake -- `macula-go`'s control
stream is documented as "one thing at a time," and a single-Session build
of this hit exactly that race live: answering inbound calls while also
making an outbound one intermittently stole the reply meant for the
outbound caller. Splitting by concern removes the race instead of hoping
timing stays lucky.

**A `pubsub watch -daemon` tap silently died within microseconds for a
long topic name, fixed 2026-08-31.** The disconnect-detector that lets
`handleWatch` notice a tap process going away read one raw byte off the
control-socket connection and treated ANY byte as "gone" -- but
`json.Encoder.Encode` (every client request's own writer) always
appends one trailing `\n`, which `Decode()` does not itself consume.
For a short request that trailing byte was reliably swallowed by the
SAME read that decoded the request body, so nothing was ever left to
find; cross whatever internal chunk-size boundary the request happens
to land on (reproduced live with a 74-byte pubsub topic -- 73 worked,
74 didn't, deterministically) and that newline is still genuinely
unread when the detector starts, closing the tap before any real event
could ever arrive -- with no error, just silence. Fixed by having the
detector tolerate exactly that one expected leftover byte instead of
treating any byte as a disconnect signal; see
`internal/daemon/pubsub.go`'s `watchForDisconnect` for the full
mechanism and receipts, and its own test file for the regression
coverage. If you're on a version older than this fix and a daemon-tap
watch on a long topic name never receives anything, this is why.

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
`-require-ucan-issuer`, `-reply`/`-echo`/`-exec`/`-exec-timeout`) are the
same ones the one-shot `serve` takes; it just sends them to the daemon
instead of dialing the mesh itself and takes no `<host[:port]>` (the
daemon already has one).

`-direct` on a daemon registration publishes the direct-dial DHT record
over the daemon's **calling** session, not its serving one. Until
2026-09-03 it used the serving session, whose receive loop belongs to
`ServeForever`, so the `put_record` reply was consumed there and every
`serve -daemon -direct` failed with `dht: put_record: connection: read
stream: deadline exceeded` while the one-shot `serve -direct` worked.
The record is still signed by, and names, the daemon's serving identity
and station; only the carrier changed. Pinned by
`internal/daemon/register_direct_live_test.go` (`go test -tags live -run
Direct ./internal/daemon`, needs a station, `MACULA_LIVE_STATION`
overrides the default Frankfurt one).

**`-exec` is the only registration mode that computes anything per
call** — `-reply`/`-echo` both answer from something already known at
registration time (a fixed payload, or the caller's own payload bounced
back), since neither this control protocol nor the daemon itself has any
other way to hand a registration a live answer. `-exec` runs the given
shell command once per inbound CALL (`sh -c` on Linux/macOS, `cmd /C` on
Windows), writing the payload as one JSON document to its stdin and
reading its entire stdout back as the reply (empty stdout replies
`null`). Every procedure a daemon serves shares its one `serveSession`
(see "Three Sessions, not one" above) — a hung exec would block every
OTHER registered procedure too, not just its own, so `-exec-timeout`
(10s default) kills it and turns the call into a normal ERROR reply
instead. A non-zero exit or invalid JSON on stdout do the same — none of
the three can crash the shared serve loop, verified live: three
procedures registered to fail three different ways (non-zero exit,
non-JSON stdout, a `sleep` past its timeout) all correctly answered
their callers with an ERROR while a fourth, working procedure kept
computing real per-call replies throughout.

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
| [`macula-io/macula-go`](https://github.com/macula-io/macula-go) | The SDK every command in this repo is built directly on — identity, wire protocol, QUIC transport. |
| [`macula-io/macula-station`](https://github.com/macula-io/macula-station) | The relay station this tool connects to and diagnoses. Its own `docs/` incident writeups are useful context for what a failure here might mean station-side. |
| `macula-apps/macula-cam2me` | The real pain that motivated this tool: mesh-connectivity issues discovered building a mobile app with no independent way to test the mesh outside the running app. |

---

## License

Dual-licensed under [Apache-2.0](LICENSE-APACHE) or [MIT](LICENSE-MIT), your choice.

---

<p align="center">
  <sub>Built on macula-go</sub>
</p>
