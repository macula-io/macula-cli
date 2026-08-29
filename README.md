# macula-cli

[![CI](https://img.shields.io/github/actions/workflow/status/macula-io/macula-cli/ci.yml?branch=master&label=CI)](https://github.com/macula-io/macula-cli/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0%20OR%20MIT-blue.svg)](#license)

A scriptable CLI for testing, monitoring, and diagnosing the [Macula](https://github.com/macula-io) mesh, built directly on [macula-go-sdk](https://github.com/macula-io/macula-go-sdk).

It exists because the SDKs give you clean primitives (connect, call, publish/subscribe, content, streaming) but no fleet-facing diagnostics of their own — no way to ask "is this station actually reachable," "did this stream actually relay across stations," or "did this content round-trip and Merkle-verify," without writing a throwaway Go program every time. This tool is that throwaway program, built once and kept.

**No TUI, no interactive mode.** The primary consumer is expected to be a script or an agent shelling out to it and parsing `--json` output, not a human watching a live dashboard. Every command works the same way with or without `--json`; human-readable output is a formatting choice, not a different code path.

Every failure is reported through Macula's own [BOLT#4 error taxonomy](https://github.com/macula-io/macula-go-sdk/blob/master/bolt4/bolt4.go) (`unknown_next_peer`, `temporary_relay_failure`, etc.) rather than invented text, so a caller parsing output gets the same failure vocabulary the wire protocol itself uses.

## Install

```
go install github.com/macula-io/macula-cli/cmd/macula-cli@latest
```

## Commands

Go's `flag` package requires flags before positional arguments — `macula-cli call --json host proc`, not `macula-cli call host proc --json`.

### `connect`

```
macula-cli connect [--json] [--timeout 15s] <host[:port]>
```

Runs the handshake as three separate, individually timed stages — DNS resolution, raw QUIC/TLS dial, and the full CONNECT/HELLO handshake — and reports exactly which stage failed. This distinction matters: macula-go-sdk's own docs record a real incident where an unhardened identity made QUIC/TLS look perfectly healthy right up until the HELLO silently never arrived, and a separate one where an IPv6-only station with no AAAA record on its hostname made a plain dial hang with no error at all. One opaque "connect failed" hides which of those happened; this command doesn't.

### `call`

```
macula-cli call [--json] [--realm <hex>] [--args '<json>'] [--timeout 15s] <host[:port]> <procedure>
```

Makes one unary RPC call and prints the RESULT payload, or the ERROR frame's BOLT#4 code/name if the call failed at the wire level. `--args` takes a JSON document; note Macula's wire protocol has no `bool` type, so a JSON boolean is rejected with an explicit error rather than silently coerced — use `0`/`1`.

### `pubsub watch`

```
macula-cli pubsub watch [--json] [--realm <hex>] [--count N] [--duration <dur>] <host[:port]> <topic>
```

Subscribes and prints events as they arrive — one JSON object per line in `--json` mode, not batched. Stops on `--count`, `--duration`, or Ctrl-C, whichever comes first. Tolerates unsolicited non-EVENT frames arriving on the control stream (a real, documented station behavior — see `pubsub.go`) instead of treating them as fatal.

### `stream probe`

```
macula-cli stream probe [--json] --provider <host[:port]> --caller <host[:port]> [--realm <hex>]
```

Opens a Bidi stream from a caller on one station to a provider on a **different** station and confirms data actually flows both ways through the relay — not just that the stream opens. This is the generalized, on-demand version of the exact check that caught a real cross-station relay bug on 2026-08-29 (STREAM_OPEN routed correctly, but DATA silently never arrived — see macula-go-sdk's `TestLiveCrossStationStreamingMultiHop`). Reports `open_routed`, `provider_received_caller_frame`, and `caller_received_provider_frame` separately, so a partial failure (open works, data doesn't) is visible instead of collapsing into one pass/fail.

### `content probe`

```
macula-cli content probe [--json] [--size 4096] [--timeout 15s] <host[:port]>
```

Puts N random bytes, gets them back, and confirms both the bytes and the Merkle verification check out — a self-contained round trip that needs no pre-existing MCID.

## Verifying a change

Every command in this repo talks to a real, live `macula-station` by design — there's nothing meaningful to unit test in isolation, and CI only checks `gofmt`/`vet`/`build`. Verify a change by actually running the affected command against the fleet (`station-de-frankfurt.macula.io:4433` is the default demo station; the full 7-station roster is documented in macula-station's own fleet notes) before considering it done, the same "run, don't scaffold" convention the sibling SDK repos follow for their own `live`-tagged tests.

## License

Dual-licensed under [Apache-2.0](LICENSE-APACHE) or [MIT](LICENSE-MIT), your choice.
