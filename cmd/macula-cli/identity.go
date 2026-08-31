package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

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
//
// "sign" is a subcommand rather than a flag on this base command
// because it produces a fundamentally different result shape (a proof,
// not an identity summary) and, unlike every other flag here, takes a
// required argument of its own (--procedure).
func runIdentity(args []string) int {
	if len(args) > 0 && args[0] == "sign" {
		return runIdentitySign(args[1:])
	}

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

type identitySignResult struct {
	NodeID    string `json:"node_id"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

// runIdentitySign produces a mailbox_ownership_proof / citizen_ownership_proof
// style ownership proof: sign {node_id, timestamp, procedure} with this
// machine's own identity, so a hecate-mail/hecate-citizens capability
// gated on proof of DID ownership can verify the caller genuinely holds
// the private key for the citizen_did they're acting as. Neither
// service's macula_response handler is handed a verified caller
// identity by hecate_om today (confirmed directly against its vendored
// macula SDK) -- this is the client half of the interim fix; a caller
// signs, the service verifies with macula_identity:verify/3 against the
// same 32-byte node_id used as citizen_did.
//
// The signed message MUST match the Erlang side's
// mailbox_ownership_proof:message/3 / citizen_ownership_proof:message/3
// byte for byte: node_id (32 raw bytes) ++ timestamp (8 bytes, big-endian)
// ++ procedure (raw UTF-8 bytes) -- no delimiters, no length prefixes,
// so both sides must agree on field widths exactly.
func runIdentitySign(args []string) int {
	fs := flag.NewFlagSet("identity sign", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	procedure := fs.String("procedure", "", "the mesh procedure this proof is for, e.g. hecate_mail.get_mailbox (required)")
	timestampMs := fs.Int64("timestamp", 0, "unix ms to sign (default: now) -- override only for testing/replay of a specific proof")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli identity sign --procedure <name> [flags]\n\n"+
			"Signs a {node_id, timestamp, procedure} ownership proof with this machine's\n"+
			"own identity, verifiable by any service using mailbox_ownership_proof or\n"+
			"citizen_ownership_proof (hecate-mail, hecate-citizens). The resulting\n"+
			"{timestamp, signature} pair is what such a service expects in a call's\n"+
			"`proof` field; `node_id` is that call's `citizen_did`.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *procedure == "" {
		fmt.Fprintln(fs.Output(), "identity sign: --procedure is required")
		fs.Usage()
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
	id, _, err := identitystore.LoadOrGenerate(path)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	ts := *timestampMs
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	sig := id.Sign(proofMessage(id.NodeID(), ts, *procedure))

	result := identitySignResult{
		NodeID:    hexNodeID(id),
		Timestamp: ts,
		Signature: hex.EncodeToString(sig),
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("node_id:   %s\n", result.NodeID)
		fmt.Printf("timestamp: %d\n", result.Timestamp)
		fmt.Printf("signature: %s\n", result.Signature)
	})
	return 0
}

func proofMessage(nodeID []byte, timestampMs int64, procedure string) []byte {
	msg := make([]byte, 0, len(nodeID)+8+len(procedure))
	msg = append(msg, nodeID...)
	msg = binary.BigEndian.AppendUint64(msg, uint64(timestampMs))
	msg = append(msg, []byte(procedure)...)
	return msg
}
