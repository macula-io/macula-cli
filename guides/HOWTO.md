# How to Use macula-cli

Every flag, default, and gotcha below is read from the actual source
(`cmd/macula-cli/*.go`) or from a real live run against the fleet, not
assumed — see the citation or the pasted output in each section if you want
to verify it yourself. §1, §2, §3 (`call`'s plain/`--direct` forms), §5, §6,
and §7 ran against the real 7-station demo fleet on 2026-08-29; §4
(`serve`), §8 (`ucan`), §9 (daemon mode, and `call`'s `-via-daemon` form)
were added and verified live on 2026-08-30.

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
identities (one per role) rather than reusing `--identity` — see §6.

**`macula-cli identity`** prints the local node ID without touching the
network — purely local, mints one via the same load-or-generate path if
none exists yet:

```
$ macula-cli identity
7facb3bdbf646393c3177fbf84b3d83dd2e5dce81235966bf8a5ae38e0ec7b47
```

**Concurrent commands need distinct `--identity` paths.** Found live
running `pubsub watch` and `pubsub publish` against each other with no
`--identity` flag on either: both defaulted to the same persisted identity
file, and the station kicked the watcher's connection the moment the
publisher connected under the same node ID (`Application error 0x0
(remote): closed`) — a real anti-duplicate-session guard, not a bug in
either command. Give concurrent invocations of this tool separate
`--identity` paths, same as the two roles in `stream probe` always get, and
the same as `serve`/`call` need when testing a provider and caller from the
same machine. Re-confirmed this exact failure shape live again on
2026-08-30 while testing daemon mode's own `pubsub watch`/`publish` pair —
it's the identity collision every time, not a regression.

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
macula-cli call [--json] [--identity <path>] [--realm <hex>] [--args '<json>'] [--timeout 15s] [--direct] [--realm-ca <pem>] [--org <name>] [--ucan <token-file>] <host[:port]> <procedure>
macula-cli call -via-daemon [--json] [--realm <hex>] [--args '<json>'] [--timeout 15s] [--ucan <token-file>] [-socket-name <name>] <procedure>
```

| Flag | Default | Meaning |
|---|---|---|
| `--realm` | all-zero (32 bytes) | 32-byte realm as hex (64 hex chars) |
| `--args` | `null` | call payload as a JSON document |
| `--timeout` | `15s` | connect + call timeout |
| `--direct` | off | resolve the procedure's DHT direct-dial advertisement and dial its server directly, instead of routing through `<host>`'s own advertise-gossip routes |
| `--realm-ca` / `--org` | — | with `--direct`, only trust an advertisement whose embedded cert chain validates to this CA and names this org (Slice 7c Direction B) — must be given together |
| `--ucan` | — | attach a token from this file to a PLAIN (non-direct) call — **not composable with `--direct`**, macula-go-sdk's direct-dial call path doesn't accept a token today |
| `-via-daemon` | off | route through a running `daemon` instead of dialing fresh — see §9. Takes no `<host[:port]>`; not composable with `--direct` |

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

**`--direct`** resolves the procedure's DHT advertisement first, then dials
the resolved station directly rather than depending on `<host>`'s own
advertise-gossip having propagated a route — pair with `serve --direct`
(§4). Output looks identical to a plain call; the difference is invisible
from the caller's own result, only in how the connection was found:

```
$ macula-cli call --json --direct station-de-frankfurt.macula.io macula_cli.howto.direct_example.1788087972
{
  "ok": true,
  "data": {
    "procedure": "macula_cli.howto.direct_example.1788087972",
    "responded_by": "f80ec2f3a21a7064bcf3e430b4cb69206c75dc52b649c7cd20ddeb44a1521865",
    "payload": {
      "via": "direct-dial"
    },
    "duration_ms": 335
  }
}
```

---

## 4. `serve` — advertise and answer

```bash
macula-cli serve [--json] [--identity <path>] [--realm <hex>] [--reply '<json>' | --echo] [--timeout 30s] [--direct] [--ttl 1h] [--cert-chain <pem>] [--require-ucan-issuer <hex>] <host[:port]> <procedure>
macula-cli serve -daemon [--json] [--reply '<json>' | --echo] [--direct] [--ttl 1h] [--cert-chain <pem>] [--require-ucan-issuer <hex>] [-socket-name <name>] <procedure>
macula-cli serve -daemon -stop [-socket-name <name>] <procedure>
```

| Flag | Default | Meaning |
|---|---|---|
| `--reply` | `null` | the RESULT payload to send back |
| `--echo` | off | reply with the caller's own payload instead of `--reply` |
| `--timeout` | `30s` | connect timeout, plus how long to wait for one inbound CALL |
| `--direct` | off | also publish a signed direct-dial DHT advertisement — pair with `call --direct` |
| `--ttl` | `1h` | direct-dial advertisement TTL (only meaningful with `--direct`) |
| `--cert-chain` | — | with `--direct`, embed this service's cert chain (Slice 7c Direction B) — requires `--direct` |
| `--require-ucan-issuer` | — | gate the procedure to callers presenting a UCAN token from this 32-byte hex Ed25519 public key — pair with `call --ucan` |
| `-daemon` | off | register with a running `daemon` instead of answering one call and exiting — persistent, many calls (§9) |

The plain (non-daemon) form advertises `<procedure>`, waits for exactly
**one** inbound CALL, answers it, and exits — the provider-role counterpart
to `call`, serving one request the same way `call` makes one. Run it in a
shell loop for a long-lived server, or use `-daemon` (§9) instead of
hand-rolling that loop:

```
$ macula-cli serve --json --reply '{"served":"yes"}' station-de-frankfurt.macula.io macula_cli.howto.serve_example.1788087920
{
  "ok": true,
  "data": {
    "procedure": "macula_cli.howto.serve_example.1788087920",
    "replied": {
      "served": "yes"
    },
    "duration_ms": 1933
  }
}
```

`--require-ucan-issuer` refuses any CALL that doesn't present a valid token
from the given issuer, **before the handler ever runs** — a refusal is
still a clean, successful exit (an ERROR frame went out over the wire, same
as any other answered call), reported as `"refused": true` rather than the
misleading shape of "served with the configured reply" an earlier draft of
this command had:

```
$ macula-cli serve --json --reply '{"secret":"granted"}' --require-ucan-issuer f80ec2f3a21a7064bcf3e430b4cb69206c75dc52b649c7cd20ddeb44a1521865 station-de-frankfurt.macula.io macula_cli.howto.gated_example
{
  "ok": true,
  "data": {
    "procedure": "macula_cli.howto.gated_example",
    "refused": true,
    "duration_ms": 2008
  }
}
```

The caller's own side of that refusal:

```
$ macula-cli call --json station-de-frankfurt.macula.io macula_cli.howto.gated_example
{
  "ok": false,
  "error": {
    "message": "call failed: unauthorized (code=16)",
    "bolt4_code": 16,
    "bolt4_name": "unauthorized",
    "retryable": false
  }
}
```

Mint the matching token with `ucan mint` (§8) and attach it via `call
--ucan <file>` to reach the same procedure successfully — the `--reply`
payload comes back exactly as configured once the token checks out.

---

## 5. `pubsub watch` / `pubsub publish` — live event stream / one-shot publish

```bash
macula-cli pubsub watch [--json] [--identity <path>] [--realm <hex>] [--count N] [--duration <dur>] [--poll-timeout 30s] [--connect-timeout 15s] <host[:port]> <topic>
macula-cli pubsub watch -daemon [--json] [--realm <hex>] [--count N] [--duration <dur>] [-socket-name <name>] <topic>
```

| Flag | Default | Meaning |
|---|---|---|
| `--count` | `0` (unbounded) | stop after this many events |
| `--duration` | `0` (unbounded) | stop watching after this long |
| `--poll-timeout` | `30s` | how long to wait for the next event before re-polling |
| `--connect-timeout` | `15s` | connect timeout |
| `-daemon` | off | tap a running daemon's own subscription instead of subscribing here (§9) |

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

### `pubsub publish`

```bash
macula-cli pubsub publish [--json] [--identity <path>] [--realm <hex>] [--payload '<json>'] [--connect-timeout 15s] <host[:port]> <topic>
```

Connects, publishes one event, and exits — no standing connection, no
subscriber acknowledgment (PUBLISH has no ack on this wire protocol, so a
clean exit means the send succeeded, not that anyone received it). `--seq`
isn't a flag: this process doesn't persist state between invocations, so
each publish uses the current time in milliseconds as its sequence number,
which is monotonic enough across separate one-shot runs.

```
$ macula-cli pubsub publish --json --payload '{"attempt":3}' station-de-frankfurt.macula.io macula_cli.smoketest.topic
{
  "ok": true,
  "data": {
    "topic": "macula_cli.smoketest.topic",
    "seq": 1788004600827,
    "duration_ms": 48
  }
}
```

**Remember §1's identity-collision gotcha**: `watch` and `publish` running
concurrently need distinct `--identity` paths, or the station will kick
whichever connected second.

**`pubsub subscribe` / `pubsub unsubscribe`** are daemon-only — there's no
non-daemon form of either, since a one-shot process subscribing then
immediately exiting has no purpose. See §9.

---

## 6. `stream probe` — cross-station streaming round trip

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

## 7. `content probe` / `put` / `get` — real content transfer

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

### `content put` / `content get`

```bash
macula-cli content put [--json] [--identity <path>] [--connect-timeout 15s] <host[:port]> <file>
macula-cli content get [--json] [--identity <path>] [--out <file>] [--connect-timeout 15s] <host[:port]> <mcid>
```

`put` uploads a real file and prints its MCID. `get` downloads and
Merkle-verifies by MCID (`content.Get` errors internally on a mismatch, so
a clean exit already means it checked out); with `--out` it writes the
bytes to a file, without it human mode prints raw bytes to stdout and
`--json` mode includes them as `content_base64` — no temp file needed for
a caller that wants the bytes in-process.

```
$ macula-cli content put --json station-de-frankfurt.macula.io ./notes.txt
{
  "ok": true,
  "data": {
    "host": "station-de-frankfurt.macula.io:4433",
    "mcid": "0155296726f758e891af4749a2afd8b3cc9221d6846ed77097363b6c87efb9862432",
    "size_bytes": 72,
    "duration_ms": 60
  }
}

$ macula-cli content get --json station-de-frankfurt.macula.io 0155296726f758e891af4749a2afd8b3cc9221d6846ed77097363b6c87efb9862432
{
  "ok": true,
  "data": {
    "host": "station-de-frankfurt.macula.io:4433",
    "mcid": "0155296726f758e891af4749a2afd8b3cc9221d6846ed77097363b6c87efb9862432",
    "size_bytes": 72,
    "content_base64": "aGVsbG8gZnJvbSBtYWN1bGEtY2xpIGNvbnRlbnQgcHV0IHRlc3Q...",
    "duration_ms": 71
  }
}
```

---

## 8. `ucan mint` / `ucan inspect` — capability tokens

```bash
macula-cli ucan mint [--json] [--identity <path>] [--expires-in <dur>] [--capability with:can ...] [--out <file>] <issuer> <audience>
macula-cli ucan inspect [--json] <token-file>
```

| Flag | Default | Meaning |
|---|---|---|
| `--expires-in` | `0` (no expiration) | token expires this long from now |
| `--capability` | none | a `with:can` entry; repeat the flag for more than one |
| `--out` | — | write the token to this file instead of printing it |

Both are purely local — no station, no network, same shape as `identity`.
`mint` self-issues and signs a token with the local identity, matching
macula-go-sdk's `ucan.Create` exactly (spec 0.10.0, confirmed against the
Erlang reference's own NIF source, not the newer incompatible 1.0.0-rc.1
IPLD spec) — the same token verifies against macula-rust-sdk,
macula-dotnet-sdk, macula-php-sdk, or the Erlang reference. `<issuer>`/
`<audience>` are opaque DID strings, not validated here. `--capability`'s
value splits on the **first** colon only — `with:can`, so
`macula_cli.smoketest.add:invoke` becomes `with="macula_cli.smoketest.add"`,
`can="invoke"`; a `with` value containing its own colon needs a different
separator inside it, not a second `--capability` colon.

```
$ macula-cli ucan mint --json --capability "macula_cli.smoketest.add:invoke" --expires-in 1h did:key:zCallerExample did:key:zProviderExample
{
  "ok": true,
  "data": {
    "token": "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCIsInVjdiI6IjAuMTAuMCJ9.eyJpc3MiOiJkaWQ6a2V5OnpDYWxsZXJFeGFtcGxlIiwiYXVkIjoiZGlkOmtleTp6UHJvdmlkZXJFeGFtcGxlIiwiZXhwIjoxNzg4MDkxNTA4LCJjYXAiOlt7IndpdGgiOiJtYWN1bGFfY2xpLnNtb2tldGVzdC5hZGQiLCJjYW4iOiJpbnZva2UifV0sInByZiI6W119.0hVmOnZsKQ0OD6jnyof8bUX0_Zbqikhi3_YNFzi99WjzohzOvIOXxKMMW89R37y8Fl6uMORkNUN9FP8Twun5Dg",
    "issuer": "did:key:zCallerExample"
  }
}
```

`inspect` decodes a token's claims **without verifying its signature**
(`ucan.Decode`) — for seeing what a token claims, never for an
authorization decision. Pass `-` to read from stdin instead of a file:

```
$ macula-cli ucan inspect did-key-example.ucan
issuer:      did:key:zCallerExample
audience:    did:key:zProviderExample
expired:     false
capability:  macula_cli.smoketest.add:invoke
```

Mint one and attach it to a gated call with `call --ucan <file>` (§3), or
have `serve --require-ucan-issuer` (§4) demand one before answering.

---

## 9. Daemon mode

Every command above is one-shot: connect, do the one thing, exit. That
works fine for `call`/`pubsub publish`/`content put`, but it means a real
long-lived `serve` needs an external shell loop, and there's no way to keep
a subscription alive without a process staying up the whole time.

**Daemon mode** is the alternative: one `macula-cli daemon start` process
holds a station connection open, and other `macula-cli` invocations control
it over a local Unix domain socket instead of each dialing the mesh fresh —
the same shape as `ssh-agent` or `dockerd`, not a second product.

```bash
macula-cli daemon start [--json] [--identity <path>] [-socket-name <name>] [-socket <path>] [--connect-timeout 15s] <host[:port]>
macula-cli daemon status [--json] [-socket-name <name>] [-socket <path>]
macula-cli daemon stop   [--json] [-socket-name <name>] [-socket <path>]
```

`daemon start` connects and blocks in the foreground until stopped —
Ctrl-C, SIGTERM, or `daemon stop` all stop it cleanly:

```
$ macula-cli daemon start station-de-frankfurt.macula.io:4433
daemon started: identity=c1ca92ae071995ebdec91fc59904df09c961e7478888aed1da918f4868a945bd connected_to=station-de-frankfurt.macula.io:4433 socket=/run/user/1000/macula-cli/default.sock
(Ctrl-C, SIGTERM, or "macula-cli daemon stop" to stop)
```

`-socket-name` lets more than one daemon instance run side by side (one per
identity/realm, say); every daemon-aware command takes it, and `-socket`
overrides the derived path outright. The socket lives under
`$XDG_RUNTIME_DIR/macula-cli` when set (systemd-logind's per-UID tmpfs,
already `0700` and correctly owned before any session starts — what the
example above actually used), or a permission-and-ownership-verified
`os.TempDir()` directory otherwise. See the
[README's own section](../README.md#daemon-mode) for the full reasoning —
this was tightened live after being asked directly whether the fallback
path was safe on a shared, multi-user Linux box (it wasn't, as first
shipped; it is now).

**Three Sessions, not one** — also load-bearing, also found live. A daemon
connects three times: one Session (the real, persisted identity) owns
serving and every advertisement; a second, ephemeral-identity Session is
dedicated to `call -via-daemon`; a third, separately ephemeral, is
dedicated to every `pubsub subscribe`d topic. A first draft sharing one
Session for everything hit macula-go-sdk's documented "control stream is
one thing at a time" limitation directly: answering inbound calls while
also making an outbound one intermittently stole the reply meant for the
outbound caller (`connection: read stream: deadline exceeded`, non-
deterministically, only under real concurrent load — see the README for
the full writeup). Splitting by concern removed it; ten `call -via-daemon`
calls back to back plus three fired concurrently against a live publish all
came back correct once fixed.

### `serve -daemon`

```bash
macula-cli serve -daemon [--json] [--reply '<json>' | --echo] [--direct] [--require-ucan-issuer <hex>] [-socket-name <name>] <procedure>
macula-cli serve -daemon -stop [-socket-name <name>] <procedure>
```

Registers `<procedure>` with a running daemon instead of answering one call
and exiting — the daemon answers as many calls as arrive until `-stop`
unregisters it or the daemon itself stops. Same advertise/UCAN flags §4
already covers; no `<host[:port]>` (the daemon already has one):

```
$ macula-cli serve -daemon -socket-name test1 -reply '{"hello":"from daemon"}' macula_cli.daemon_smoke_test
macula_cli.daemon_smoke_test registered with the daemon at /run/user/1000/macula-cli/test1.sock

$ macula-cli call station-de-frankfurt.macula.io:4433 macula_cli.daemon_smoke_test
macula_cli.daemon_smoke_test -> 19d82767a5f64ae98490846a12394860b6b79dda54f0852ca56952fa38826dd8 (28 ms)
  map[hello:from daemon]

$ macula-cli call station-de-frankfurt.macula.io:4433 macula_cli.daemon_smoke_test
macula_cli.daemon_smoke_test -> 19d82767a5f64ae98490846a12394860b6b79dda54f0852ca56952fa38826dd8 (28 ms)
  map[hello:from daemon]
```

Two identical calls in a row, both answered — proving persistence, not
one-shot. After `-stop`, the same call correctly fails again:

```
$ macula-cli serve -daemon -socket-name test1 -stop macula_cli.daemon_smoke_test
macula_cli.daemon_smoke_test unregistered

$ macula-cli call station-de-frankfurt.macula.io:4433 macula_cli.daemon_smoke_test
error: call failed: unknown_next_peer (code=1) (bolt4=unknown_next_peer, retryable=true)
```

### `call -via-daemon`

```bash
macula-cli call -via-daemon [--json] [--args '<json>'] [--ucan <token-file>] [-socket-name <name>] <procedure>
```

Routes the call through the daemon's own (ephemeral-identity) connection
instead of dialing fresh. Not composable with `--direct` — direct-dial
resolves and dials a different station per call, unrelated to whatever the
daemon happens to be connected to. `--json` output is identical to a plain
call, including BOLT#4 detail on a wire-level failure:

```
$ macula-cli call -via-daemon -socket-name fix -json macula_cli.daemon_fix_test.call
{
  "ok": true,
  "data": {
    "procedure": "macula_cli.daemon_fix_test.call",
    "responded_by": "c1ca92ae071995ebdec91fc59904df09c961e7478888aed1da918f4868a945bd",
    "payload": { "answer": 42 },
    "duration_ms": 26
  }
}

$ macula-cli call -via-daemon -socket-name fix -json macula_cli.daemon_fix_test.nope
{
  "ok": false,
  "error": {
    "message": "daemon: call.invoke: call failed: unknown_next_peer (code=1)",
    "bolt4_code": 1,
    "bolt4_name": "unknown_next_peer",
    "retryable": true
  }
}
```

### `pubsub subscribe` / `watch -daemon` / `unsubscribe`

```bash
macula-cli pubsub subscribe   [-socket-name <name>] <topic>
macula-cli pubsub watch -daemon [--json] [--count N] [--duration <dur>] [-socket-name <name>] <topic>
macula-cli pubsub unsubscribe [-socket-name <name>] <topic>
```

`subscribe` creates a durable, daemon-owned subscription that outlives the
command that created it. `watch -daemon` taps into one — creating it first
if it doesn't already exist — and streams events until it disconnects or
the subscription ends; **it does not end the subscription itself**, only
`unsubscribe` does:

```
$ macula-cli pubsub subscribe -socket-name full macula_cli.daemon_smoke_test.pubsub
subscribed to "macula_cli.daemon_smoke_test.pubsub" via the daemon at /run/user/1000/macula-cli/full.sock

$ macula-cli pubsub watch -daemon -socket-name full -json macula_cli.daemon_smoke_test.pubsub &

$ macula-cli pubsub publish --payload '{"greeting":"hello via daemon"}' station-de-frankfurt.macula.io:4433 macula_cli.daemon_smoke_test.pubsub
published to "macula_cli.daemon_smoke_test.pubsub" (seq=1788087142460, 47 ms)

# the backgrounded watch printed:
{"topic":"macula_cli.daemon_smoke_test.pubsub","publisher":"9814c995407e43f51772d376df139c5f7f9cb8f22e196a3c3826f96d19b218ec","seq":1788087142460,"payload":{"greeting":"hello via daemon"},"delivered_via":"direct","received_at":"2026-08-30T10:52:22.474772092Z"}

$ macula-cli pubsub unsubscribe -socket-name full macula_cli.daemon_smoke_test.pubsub
macula_cli.daemon_smoke_test.pubsub unsubscribed
# the backgrounded watch then exited on its own (connection closed by the daemon)
```

`daemon status` shows both what's being served and what's subscribed:

```
$ macula-cli daemon status -socket-name full
identity:     c180c0c28b527029fe0039e0267fd3885a141271b41d4eefcfdaa9dfea2c66ba
connected to: station-de-frankfurt.macula.io:4433
uptime:       9s
serving:      (nothing registered)
subscribed:
  - macula_cli.daemon_smoke_test.pubsub
```

---

## 10. Reading a BOLT#4 error

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
| `unauthorized` | A UCAN capability check failed on a gated procedure | Not something this tool grants — mint a token with `ucan mint` (§8) covering what the procedure requires, and attach it via `call --ucan` |

---

## 11. See also

- [`README.md`](../README.md) — what macula-cli is, architecture overview, relationship to the other repos
- [`macula-go-sdk`](https://github.com/macula-io/macula-go-sdk) — the SDK every command in this repo is built directly on; its own `plans/PLAN_WIRE_PROTOCOL.md` documents the wire protocol these commands exercise
- [`macula-station`](https://github.com/macula-io/macula-station)'s [`docs/`](https://github.com/macula-io/macula-station/tree/master/docs) — real production incidents (`CASCADE_INVESTIGATION.md`, `PUBSUB_RESIGN_LOOP_LESSON.md`, and others) that are useful context for what a `stream probe`/`call` failure might actually mean on the station side
