package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/ownershipproof"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
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

// proveOwnership is payload with an ownership proof v2 (mcl-om#7) by which
// key's node authorises its fields for procedure in realm, now, once.
func proveOwnership(key *identity.NodeKey, realm [32]byte, procedure string, payload cbor.Value) (cbor.Value, error) {
	return ownershipproof.Attach(payload, key, realm, procedure)
}

func runProveOwnership(args []string) int {
	fs := flag.NewFlagSet("identity prove-ownership", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	procedure := fs.String("procedure", "", "the procedure the payload is for, e.g. mcl-graph/learn_link")
	payloadText := fs.String("payload", "", "the payload as JSON: an object, without \"caller\"")
	payloadFile := fs.String("payload-file", "", "read the payload JSON from this file instead")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli identity prove-ownership -realm <realm> -procedure <name> -payload <json> [flags]")
		fmt.Fprintln(fs.Output(), "       prints the payload with an asserted_by block (ownership proof v2) by which this node authorises")
		fmt.Fprintln(fs.Output(), "       every field, for that procedure and realm, once; send it within 60 s")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *procedure == "" || (*payloadText == "" && *payloadFile == "") {
		fs.Usage()
		return 2
	}
	payload, err := payloadFlag(*payloadText, *payloadFile)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	key, err := m.key()
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	signed, err := proveOwnership(key, realm, *procedure, payload)
	if errors.Is(err, ownershipproof.ErrNotAMap) || errors.Is(err, ownershipproof.ErrCallerField) {
		return report.Usage(m.jsonOut, err)
	}
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	text := rawJSON(wirevalue.ToJSON(signed))
	report.Ok(m.jsonOut, map[string]any{"payload": text}, func(w io.Writer) { fmt.Fprintln(w, string(text)) })
	return 0
}

func runIdentity(args []string) int {
	if len(args) > 0 && args[0] == "prove-ownership" {
		return runProveOwnership(args[1:])
	}
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, false)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli identity [-identity <file>] [-profile pq_hybrid|pq_pure] [-json]")
		fmt.Fprintln(fs.Output(), "       the node key macula-cli joins the mesh with, created on first use")
		fmt.Fprintln(fs.Output(), "       macula-cli identity prove-ownership -h: sign a payload's asserted_by (ownership proof v2)")
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
