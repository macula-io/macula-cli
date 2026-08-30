package main

import (
	"flag"
	"fmt"

	"github.com/macula-io/macula-cli/internal/identitystore"
	"github.com/macula-io/macula-cli/internal/report"
)

type identityResult struct {
	NodeID    string `json:"node_id"`
	Path      string `json:"path"`
	Generated bool   `json:"generated"`
}

// runIdentity is purely local -- no station, no network. It exists so a
// caller (an MCP server shelling out to this binary is the motivating
// case) can learn this machine's node ID without that being a side
// effect buried inside some other command's connect step.
func runIdentity(args []string) int {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli identity [flags]\n\n"+
			"Prints this machine's local identity (node ID), minting one via the same\n"+
			"load-or-generate path every other command uses if none exists yet.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := *identityPath
	if path == "" {
		var err error
		path, err = identitystore.DefaultPath()
		if err != nil {
			return report.Fail(*jsonOut, err, nil)
		}
	}
	id, generated, err := identitystore.LoadOrGenerate(path)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	result := identityResult{
		NodeID:    hexNodeID(id),
		Path:      path,
		Generated: generated,
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Println(result.NodeID)
	})
	return 0
}
