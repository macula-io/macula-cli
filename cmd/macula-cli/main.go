// Command macula-cli is a scriptable client for testing, monitoring,
// and diagnosing the Macula mesh, built directly on macula-go.
// Every subcommand accepts --json for structured output and reports
// failures through Macula's own BOLT#4 error vocabulary rather than
// invented text. It has no interactive/TUI mode by design: the primary
// consumer is expected to be a script or an agent shelling out to it,
// not a human watching a live dashboard.
package main

import (
	"fmt"
	"os"
)

// version, commit, and date are set via -ldflags by .goreleaser.yml at
// release build time; "dev" is what `go build`/`go run` without those
// flags produces, which is the honest answer for a local build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}

	switch args[0] {
	case "connect":
		return runConnect(args[1:])
	case "call":
		return runCall(args[1:])
	case "serve":
		return runServe(args[1:])
	case "pubsub":
		return runPubsub(args[1:])
	case "stream":
		return runStream(args[1:])
	case "content":
		return runContent(args[1:])
	case "dht":
		return runDht(args[1:])
	case "identity":
		return runIdentity(args[1:])
	case "ucan":
		return runUcan(args[1:])
	case "daemon":
		return runDaemon(args[1:])
	case "-v", "--version", "version":
		fmt.Printf("macula-cli %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "macula-cli: unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `macula-cli — test, monitor, and diagnose the Macula mesh

Usage:
  macula-cli connect <host[:port]>                    staged handshake diagnostic (DNS, QUIC, HELLO)
  macula-cli call <host[:port]> <procedure>            unary RPC call
  macula-cli call -via-daemon <procedure>              same, routed through a running daemon
  macula-cli serve <host[:port]> <procedure>           advertise, answer one inbound CALL, exit
  macula-cli serve -daemon <procedure>                 register with a running daemon, answer many calls
  macula-cli pubsub watch <host[:port]> <topic>        subscribe and print events as they arrive
  macula-cli pubsub watch -daemon <topic>              tap a daemon's own subscription
  macula-cli pubsub publish <host[:port]> <topic>      publish one event to a topic
  macula-cli pubsub subscribe <topic>                  daemon-only: start a durable subscription
  macula-cli pubsub unsubscribe <topic>                daemon-only: end a durable subscription
  macula-cli stream probe                              cross-station streaming round trip
  macula-cli content probe <host[:port]>               content put/get/verify round trip
  macula-cli content put <host[:port]> <file>          upload a file, print its MCID
  macula-cli content get <host[:port]> <mcid>          download by MCID
  macula-cli dht find-record <host[:port]> <key-hex>   fetch one DHT record by storage key
  macula-cli dht find-records <host[:port]> <key-hex>  fetch every record at a storage key
  macula-cli dht find-records-by-type <host[:port]> <type>  list every record of a type (discovery)
  macula-cli identity                                  print the local identity's node ID
  macula-cli identity sign --procedure <name>          sign a {node_id, timestamp, procedure} ownership proof
  macula-cli ucan mint <issuer> <audience>             mint a UCAN token, signed by the local identity
  macula-cli ucan inspect <token-file>                 decode a UCAN token's claims (no signature check)
  macula-cli daemon start <host[:port]>                hold one Session open, serve registered procedures
  macula-cli daemon status                             show what a running daemon is serving/subscribed to
  macula-cli daemon stop                               ask a running daemon to shut down

Run "macula-cli <command> -h" for a command's own flags.
`)
}
