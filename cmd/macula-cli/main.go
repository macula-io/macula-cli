// Command macula-cli is a scriptable client of the macula 12 mesh, built on
// macula-go: link to stations, call and serve procedures, publish and watch,
// read the DHT, share and fetch content, and join a realm. Every command
// takes -json for a structured envelope, whose failures carry a fixed kind.
// It has no interactive mode by design: its consumers are scripts and agents.
package main

import (
	"fmt"
	"os"
)

// version, commit and date are set by .goreleaser.yml at release; "dev" is
// what a plain go build gives.
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
	case "realm":
		return runRealm(args[1:])
	case "-v", "--version", "version":
		fmt.Printf("macula-cli %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "-h", "--help", "help":
		usage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "macula-cli: unknown command %q\n\n", args[0])
	usage()
	return 2
}

func usage() {
	fmt.Fprint(os.Stderr, `macula-cli: a scriptable client of the macula 12 mesh

Every station is pinned: -seed host[:port]@<station node_id hex>.
A realm is -realm <name or 64-hex id>, trusted with -realm-key <hex|@file>.
A procedure ~/<name> is <name> in this node's own namespace.

  macula-cli connect -seed ...                         resolve the seed and link to its station
  macula-cli call -seed ... -realm ... <procedure>      call a procedure by direct dial
  macula-cli serve -seed ... -realm ... <procedure>     serve a procedure (echo, or -reply), until stopped
  macula-cli pubsub publish -seed ... -realm ... <topic>
  macula-cli pubsub watch -seed ... -realm ... <topic>  print each verified event
  macula-cli stream probe -seed ... -realm ...          a streaming round trip between two fresh nodes
  macula-cli content share -seed ... -realm ... <file>  serve a file from this node, print its content id
  macula-cli content get -seed ... -realm ... <mcid>    fetch content, checked against its id
  macula-cli content probe -seed ... -realm ...         share and fetch between two fresh nodes
  macula-cli dht find-record|find-records -seed ... <key hex>
  macula-cli dht find-records-by-type -seed ... <type>  node_record, station_endpoint, ...
  macula-cli identity                                   this node's key: node_id, key id, profile
  macula-cli realm join -realm <name>                   ask a realm to admit this device (a human admits it)
  macula-cli realm status <session id>                  a join session's state
  macula-cli realm membership -seed ... -realm <name> -realm-key ...
                                                        this node's membership UCAN, over the mesh

Run "macula-cli <command> -h" for a command's flags.
`)
}
