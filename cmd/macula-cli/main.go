// Command macula-cli is a scriptable client for testing, monitoring,
// and diagnosing the Macula mesh, built directly on macula-go-sdk.
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
	case "pubsub":
		return runPubsub(args[1:])
	case "stream":
		return runStream(args[1:])
	case "content":
		return runContent(args[1:])
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
  macula-cli pubsub watch <host[:port]> <topic>        subscribe and print events as they arrive
  macula-cli stream probe                              cross-station streaming round trip
  macula-cli content probe <host[:port]>               content put/get/verify round trip

Run "macula-cli <command> -h" for a command's own flags.
`)
}
