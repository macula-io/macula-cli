# How to Use macula-cli

Every flag, default, and gotcha below is read from the actual source
(`cmd/macula-cli/*.go`) or from a real live run against the fleet, not
assumed — see the citation or the pasted output in each section if you want
to verify it yourself. All examples below ran against the real 7-station
demo fleet on 2026-08-29.

**One cross-cutting gotcha before anything else: Go's `flag` package stops
parsing at the first non-flag argument.** `macula-cli call --json host proc`
works; `macula-cli call host proc --json` does not — the `--json` after the
positional args is silently treated as a third positional and the command
fails its arg-count check. Flags always go before positionals, for every
command in this repo.

---

## 1. Installing and identity

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/macula-io/macula-cli/master/install.sh | bash
```

```powershell
# Windows
irm https://raw.githubusercontent.com/macula-io/macula-cli/master/install.ps1 | iex
```

Both scripts download the release archive matching your OS/arch from
[GitHub Releases](https://github.com/macula-io/macula-cli/releases),
verify it against that release's own `checksums.txt` (SHA-256), and install
the binary — `$HOME/.local/bin` by default on Linux/macOS,
`%LOCALAPPDATA%\macula-cli` on Windows, overridable via
`MACULA_CLI_INSTALL_DIR`. Pin a specific version instead of latest with
`MACULA_CLI_VERSION=v0.1.0` (or `$env:MACULA_CLI_VERSION` on Windows) before
the install command.

Already have Go, or prefer building from source:

```bash
go install github.com/macula-io/macula-cli/cmd/macula-cli@latest
```

Most commands need an identity to connect with. Every command that does
takes `--identity <path>`; if you don't pass one, macula-cli mints a fresh
puzzle-hardened Ed25519 identity on first use and persists it via Go's
`os.UserConfigDir()` — `$XDG_CONFIG_HOME/macula-cli/identity.seed` (or
`~/.config/macula-cli/identity.seed`) on Linux, `~/Library/Application
Support/macula-cli/identity.seed` on macOS, `%AppData%\macula-cli\
identity.seed` on Windows (note: **not** the same directory `install.ps1`
puts the binary in, `%LOCALAPPDATA%`) — reusing it on every later run. First
use prints `(generated a new identity — puzzle grinding took a moment)` to
say why it paused — puzzle grinding (S/Kademlia Sybil-resistance proof of
work) is real CPU work, typically well under a second but not
instantaneous.

**Uninstalling:**

```bash
# Linux / macOS — leaves the persisted identity in place
curl -fsSL https://raw.githubusercontent.com/macula-io/macula-cli/master/uninstall.sh | bash
# add --purge to remove the identity too:
curl -fsSL .../uninstall.sh | bash -s -- --purge
```

```powershell
# Windows — leaves the persisted identity in place
irm https://raw.githubusercontent.com/macula-io/macula-cli/master/uninstall.ps1 | iex
# -Purge needs a local copy first (piped iex can't take script params):
iwr -useb .../uninstall.ps1 -OutFile uninstall.ps1; .\uninstall.ps1 -Purge
```

Both remove the binary from wherever `install.sh`/`install.ps1` put it
(respecting `MACULA_CLI_INSTALL_DIR` if you overrode it at install time)
and, by default, leave the persisted identity file untouched — it took real
puzzle-grinding work to generate, and a later reinstall should be able to
reuse it rather than mint a new one silently. Pass `--purge` (`-Purge` on
Windows) to remove that too.

`stream probe` is the one exception: it always mints two fresh, unpersisted
identities (one per role) rather than reusing `--identity` — see §5.

---

## 2. `connect` — staged handshake diagnostic

```bash
macula-cli connect [--json] [--identity <path>] [--timeout 15s] <host[:port]>
```

| Flag | Default | Meaning |
|---|---|---|
| `--json` | off | JSON envelope instead of human text |
| `--identity` | config dir | path to a persisted identity seed |
| `--timeout` | `15s` | timeout for each of the three stages below, independently |

Runs the handshake as three separately timed stages — DNS, raw QUIC/TLS,
then the full CONNECT/HELLO handshake — and reports which one failed, rather
than one opaque "connect failed."

**Why this is three stages and not one call**: macula-go-sdk's own docs
record two real silent-failure classes that a single pass/fail result would
hide completely — an unhardened identity that makes QUIC/TLS look perfectly
healthy right up until the HELLO never arrives, and an IPv6-only station
with no `AAAA` record on its hostname that makes a plain dial hang with no
error at all. Both would look identical from the outside without staging.

```
$ macula-cli connect station-fr-paris.macula.io
station-fr-paris.macula.io:4433
  dns    ok  (34 ms) A=[] AAAA=[2600:3c1a:e001:19::be:2]
  quic   ok  (32 ms)
  hello  ok  (24 ms) station=3ed457a593db849f0e27f4f1a1a81c1fe927ad16d1fa8bf397bbdc1b013f1d8a accepted=true
```

A DNS failure short-circuits immediately, before QUIC is ever attempted:

```
$ macula-cli connect --json this-host-does-not-exist.macula.io
{
  "ok": false,
  "error": {
    "message": "lookup this-host-does-not-exist.macula.io: no such host"
  }
}
```

---

## 3. `call` — unary RPC

```bash
macula-cli call [--json] [--identity <path>] [--realm <hex>] [--args '<json>'] [--timeout 15s] <host[:port]> <procedure>
```

| Flag | Default | Meaning |
|---|---|---|
| `--realm` | all-zero (32 bytes) | 32-byte realm as hex (64 hex chars) |
| `--args` | `null` | call payload as a JSON document |
| `--timeout` | `15s` | connect + call timeout |

**Macula's wire protocol has no `bool` type at all** (see macula-go-sdk's
`cbor` package doc) — `--args '{"active": true}'` is rejected outright with
an explicit error rather than silently coerced into something that isn't
what you typed:

```
$ macula-cli call --args '{"active":true}' station-de-frankfurt.macula.io some.procedure
error: wirevalue: JSON boolean true has no wire representation (macula's CBOR has no bool type) — use 0/1 instead
```

A call against a procedure nothing has advertised comes back as a normal
BOLT#4 error, not a crash — this is the expected shape of "nobody's
listening," and it's what `macula-go-sdk`'s own `examples/quickstart`
currently gets calling `io.macula.echo` too, since that procedure isn't
presently advertised on the demo fleet:

```
$ macula-cli call station-de-frankfurt.macula.io io.macula.echo
error: call failed: unknown_next_peer (code=1) (bolt4=unknown_next_peer, retryable=true)
```

Against a procedure that *is* being served, the RESULT payload prints
directly:

```
$ macula-cli call --json --args '{"a":10,"b":32}' station-de-frankfurt.macula.io macula_cli.smoketest.add
{
  "ok": true,
  "data": {
    "procedure": "macula_cli.smoketest.add",
    "responded_by": "ab64de5b636ba06d148c9a8bf912f6bc70746ef48c305e0ee9347e6ada5975ce",
    "payload": 42,
    "duration_ms": 26
  }
}
```

**A gotcha worth knowing if you're testing your own provider**: if the
provider's `Advertise` hasn't finished registering with the station yet when
the call fires, you'll see a local timeout (`connection: read stream:
deadline exceeded`) rather than a clean `unknown_next_peer` — leave a
second or two of margin between starting a test provider and calling into
it, same-station advertises are near-instant but not instant.

---

## 4. `pubsub watch` — live event stream

```bash
macula-cli pubsub watch [--json] [--identity <path>] [--realm <hex>] [--count N] [--duration <dur>] [--poll-timeout 30s] [--connect-timeout 15s] <host[:port]> <topic>
```

| Flag | Default | Meaning |
|---|---|---|
| `--count` | `0` (unbounded) | stop after this many events |
| `--duration` | `0` (unbounded) | stop watching after this long |
| `--poll-timeout` | `30s` | how long to wait for the next event before re-polling |
| `--connect-timeout` | `15s` | connect timeout |

Stops on `--count`, `--duration`, or Ctrl-C, whichever comes first.
`--json` mode prints one JSON object per line as each event arrives
(newline-delimited, not a batched array), so a consumer can start acting on
the first event without waiting for the watch to end.

```
$ macula-cli pubsub watch --json --count 1 --duration 25s station-de-frankfurt.macula.io macula_cli.smoketest.topic
{"topic":"macula_cli.smoketest.topic","publisher":"fae9598a1bae2a29e5adae396fe8e590aaba108cd75c2bddeb39d108b2554821","seq":1,"payload":"hello from smoketest publisher","delivered_via":"direct","received_at":"2026-08-29T06:42:43.320600429Z"}
```

### The gotcha that will bite you once: the control stream isn't event-exclusive

**A subscribed session's control stream can carry frames that aren't
EVENTs at all — a real station behavior, not a bug in this tool.** A
sibling SDK (`macula-dotnet-sdk`) independently documented "unprompted
advertise frames for built-in `_content.*` procedures on every client's
control stream" while building its own examples; `pubsub watch` hit the
exact same thing live while this command was first being tested, and
crashed with:

```
{"ok":false,"error":{"message":"frame_type is not \"event\""}}
```

**The fix** (already in the shipped code, not something you need to work
around): `session.RecvEvent`'s `frame.ErrNotAnEventFrame` is treated as
"skip this frame, keep waiting," not fatal — `RecvFrame` has already
consumed it, so the next call just waits for whatever arrives next. If
you're extending this command or writing something similar against
`RecvEvent` directly, don't treat every non-timeout error from it as fatal;
check for `frame.ErrNotAnEventFrame` first.

---

## 5. `stream probe` — cross-station streaming round trip

```bash
macula-cli stream probe [--json] --provider <host[:port]> --caller <host[:port]> [--realm <hex>] [--procedure <name>] [--propagation-wait 8s] [--accept-timeout 30s] [--connect-timeout 15s]
```

| Flag | Default | Meaning |
|---|---|---|
| `--provider` | *(required)* | station the provider role connects to |
| `--caller` | *(required)* | station the caller role connects to — must differ from `--provider` to actually exercise cross-station relay |
| `--procedure` | random `macula_cli.probe.*` | procedure name to advertise/open |
| `--propagation-wait` | `8s` | time to let the advertise gossip-propagate before the caller opens |
| `--accept-timeout` | `30s` | how long the provider waits for the relayed `STREAM_OPEN` |

This is the generalized, on-demand version of the exact check that caught a
real cross-station relay bug on 2026-08-29: `STREAM_OPEN` routed correctly
across stations, but the `STREAM_DATA` frame that followed silently never
arrived (root cause: a missing `signer` field a second relay hop needs — see
`macula-go-sdk`'s `TestLiveCrossStationStreamingMultiHop` for the full
writeup). This command reports the three stages of that scenario
**separately** — `open_routed`, `provider_received_caller_frame`,
`caller_received_provider_frame` — instead of one pass/fail, so a partial
failure (open works, data doesn't) stays visible instead of collapsing into
an ambiguous "it failed."

```
$ macula-cli stream probe --json --provider station-de-frankfurt.macula.io --caller station-it-milan.macula.io
{
  "ok": true,
  "data": {
    "provider": "station-de-frankfurt.macula.io",
    "caller": "station-it-milan.macula.io",
    "procedure": "macula_cli.probe.65057454d117efc6",
    "open_routed": true,
    "provider_received_caller_frame": true,
    "caller_received_provider_frame": true,
    "duration_ms": 8209
  }
}
```

**Identities are always fresh here, never `--identity`**: a probe simulates
two distinct peers (a provider and a caller), so reusing the operator's own
standing identity for both roles wouldn't represent anything real. Each run
grinds two puzzle-hardened identities and discards them.

**On `--propagation-wait`**: 8s (not 5s) is deliberate, found by testing —
running several probes back to back in the same process intermittently hit
`Accept` timeouts at 5s/20s that reordering proved were about *execution
position*, not the station pair or the relay itself (a client-side
session-teardown timing artifact between rapid sequential live connects,
see `macula-go-sdk`'s own commit `4a11478`). If you're scripting many probes
in a tight loop, leave a real gap between them rather than trusting a lower
`--propagation-wait` to save time.

---

## 6. `content probe` — put/get/verify round trip

```bash
macula-cli content probe [--json] [--identity <path>] [--size 4096] [--connect-timeout 15s] <host[:port]>
```

| Flag | Default | Meaning |
|---|---|---|
| `--size` | `4096` | bytes of random test content to round-trip |

Puts N random bytes, gets them back, and confirms both the raw bytes and
the Merkle verification (implicit inside a clean `content.Get` — it errors
on mismatch internally) check out. No pre-existing MCID needed; it
generates its own test content each run.

```
$ macula-cli content probe --json station-de-frankfurt.macula.io
{
  "ok": true,
  "data": {
    "host": "station-de-frankfurt.macula.io:4433",
    "mcid": "01554ff4f8ed75c7f6148b30b0a3b10918f2f684f98ab848b49214bf58d4f85d23d3",
    "size_bytes": 4096,
    "bytes_matched": true,
    "duration_ms": 28
  }
}
```

---

## 7. Reading a BOLT#4 error

Every wire-level `call`/`stream probe` failure carries the code Macula's
[BOLT#4 taxonomy](https://github.com/macula-io/macula-go-sdk/blob/master/bolt4/bolt4.go)
defines, printed as `bolt4=<name>` in human mode or `bolt4_name` /
`bolt4_code` / `retryable` in `--json`. The ones you'll actually run into
testing against the demo fleet:

| Name | What it means | What to check |
|---|---|---|
| `unknown_next_peer` | Nobody's advertised this procedure anywhere the station's routing table knows about | Is the provider actually running and did it `Advertise` before you called? Right `--realm`? |
| `temporary_relay_failure` | A relay hop hit a transient problem | Retryable — try again; if it persists, see if `stream probe` between the same two stations reproduces it |
| `target_realm_refused` | The target station doesn't serve the realm you asked for | Double-check `--realm`'s hex against what the provider actually advertised under |
| `unauthorized` | A UCAN capability check failed on a gated procedure | Not something this tool grants — the procedure needs a capability this identity doesn't have |

---

## 8. See also

- [`README.md`](../README.md) — what macula-cli is, architecture overview, relationship to the other repos
- [`macula-go-sdk`](https://github.com/macula-io/macula-go-sdk) — the SDK every command in this repo is built directly on; its own `plans/PLAN_WIRE_PROTOCOL.md` documents the wire protocol these commands exercise
- [`macula-station`](https://github.com/macula-io/macula-station)'s [`docs/`](https://github.com/macula-io/macula-station/tree/master/docs) — real production incidents (`CASCADE_INVESTIGATION.md`, `PUBSUB_RESIGN_LOOP_LESSON.md`, and others) that are useful context for what a `stream probe`/`call` failure might actually mean on the station side
