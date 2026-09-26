package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/macula-io/macula-go/identity"

	"github.com/macula-io/macula-cli/internal/report"
)

// identityResult is the node key: its node_id, key id, profile and carried
// public key's size.
type identityResult struct {
	NodeID         string `json:"node_id"`
	KeyID          string `json:"key_id"`
	Profile        string `json:"profile"`
	PublicKeyBytes int    `json:"public_key_bytes"`
}

func describeKey(key *identity.NodeKey) (identityResult, error) {
	id, err := key.NodeID()
	if err != nil {
		return identityResult{}, err
	}
	keyID := key.KeyID()
	return identityResult{NodeID: fmt.Sprintf("%x", id), KeyID: fmt.Sprintf("%x", keyID), Profile: string(key.Profile()),
		PublicKeyBytes: len(key.PublicKey())}, nil
}

func runIdentity(args []string) int {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, false)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli identity [-identity <file>] [-profile pq_hybrid|pq_pure] [-json]")
		fmt.Fprintln(fs.Output(), "       the node key macula-cli joins the mesh with, created on first use")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	key, err := m.key()
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	r, err := describeKey(key)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, r, func(w io.Writer) {
		fmt.Fprintf(w, "node_id %s\nkey_id  %s\nprofile %s (public key %d bytes)\n", r.NodeID, r.KeyID, r.Profile, r.PublicKeyBytes)
	})
	return 0
}
