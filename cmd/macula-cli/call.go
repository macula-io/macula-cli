package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/pool"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// callResult is what a call reports, with its seal report: Sealed 1 when the
// exchange was sealed end to end, to the key SealKeyID names (empty when
// clear), by Provider.
type callResult struct {
	Procedure string  `json:"procedure"`
	Result    rawJSON `json:"result"`
	Sealed    int     `json:"sealed"`
	Provider  string  `json:"provider"`
	SealKeyID string  `json:"seal_key_id"`
	value     cbor.Value
}

// presented is what a call shows a gated procedure: a UCAN for this node and
// its chain's parents (none for an open procedure).
type presented struct {
	token  []byte
	proofs [][]byte
}

// call calls procedure in realm, at provider when it is not zero, by direct
// dial, presenting chain's token when it has one. It seals to the key the
// provider's advertisement names, if any.
func call(ctx context.Context, p *pool.Pool, realm [32]byte, procedure string, payload cbor.Value, provider [32]byte,
	m *meshFlags, chain presented) (callResult, error) {
	procedure = ownProcedure(procedure, p.NodeID())
	result, rep, err := p.CallReport(ctx, pool.Call{Realm: realm, Procedure: procedure, Provider: provider,
		Payload: payload, Timeout: m.timeout, Token: chain.token, Proofs: chain.proofs})
	if err != nil {
		return callResult{}, err
	}
	r := callResult{Procedure: procedure, Result: rawJSON(wirevalue.ToJSON(result)), value: result,
		Sealed: rep.Sealed, Provider: fmt.Sprintf("%x", rep.Provider)}
	if rep.Sealed == 1 {
		r.SealKeyID = fmt.Sprintf("%x", rep.SealKeyID)
	}
	return r, nil
}

// sealLine is the seal report in text mode, on stderr.
func sealLine(r callResult) string {
	if r.Sealed == 1 {
		return fmt.Sprintf("sealed to key %s of provider %s", r.SealKeyID, r.Provider)
	}
	return fmt.Sprintf("not sealed (provider %s names no key)", r.Provider)
}

func runCall(args []string) int {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	payloadText := fs.String("payload", "null", "the payload as JSON (bytes as {\"$bytes\": base64}); no booleans")
	payloadFile := fs.String("payload-file", "", "read the payload JSON from this file instead")
	providerHex := fs.String("provider", "", "call this provider's node_id (64 hex); any trusted provider when absent")
	ucanFile := fs.String("ucan-file", "", "present the UCAN chain in this file (token, then its proofs, a line each) to a gated procedure")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli call -seed host:port@<node_id> -realm <realm> [-realm-key <hex|@file>] [flags] <procedure>")
		fmt.Fprintln(fs.Output(), "       a procedure '~/<name>' (quoted) is <name> in this node's own namespace")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &m.jsonOut, exactly(1)); !ok {
		return code
	}
	payload, err := payloadFlag(*payloadText, *payloadFile)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	var provider [32]byte
	if *providerHex != "" {
		if provider, err = hex32(*providerHex); err != nil {
			return report.Usage(m.jsonOut, fmt.Errorf("-provider: %w", err))
		}
	}
	var chain presented
	if *ucanFile != "" {
		if chain.token, chain.proofs, err = readChain(*ucanFile); err != nil {
			return report.Usage(m.jsonOut, err)
		}
	}
	ctx := context.Background()
	p, err := m.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	result, err := call(ctx, p, realm, fs.Arg(0), payload, provider, &m, chain)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, result, func(w io.Writer) {
		fmt.Fprintln(w, string(result.Result))
		fmt.Fprintln(os.Stderr, sealLine(result))
	})
	return 0
}

// payloadFlag is -payload, or -payload-file when given.
func payloadFlag(text, file string) (cbor.Value, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return cbor.Value{}, fmt.Errorf("-payload-file: %w", err)
		}
		text = string(raw)
	}
	v, err := wirevalue.FromJSON([]byte(text))
	if err != nil {
		return cbor.Value{}, fmt.Errorf("the payload: %w", err)
	}
	return v, nil
}

// rawJSON is JSON already encoded, emitted as is in a --json envelope.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }
