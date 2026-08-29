package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"github.com/macula-io/macula-go-sdk/bolt4"
	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/transport"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

type callResult struct {
	Procedure   string `json:"procedure"`
	RespondedBy string `json:"responded_by"`
	Payload     any    `json:"payload"`
	DurationMs  int64  `json:"duration_ms"`
}

func runCall(args []string) int {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	argsJSON := fs.String("args", "null", "call payload as a JSON document")
	timeout := fs.Duration("timeout", 15*time.Second, "connect + call timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli call [flags] <host[:port]> <procedure>\n\n"+
			"Makes one unary RPC call and prints the RESULT payload, or the ERROR frame's\n"+
			"BOLT#4 code/name if the call failed at the wire level.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}

	host, port, err := parseHostPort(fs.Arg(0))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	procedure := fs.Arg(1)

	realm, err := parseRealm(*realmHex)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	payload, err := wirevalue.FromJSON([]byte(*argsJSON))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	session, err := connection.Connect(ctx, host, port, transport.WebPKI{}, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	start := time.Now()
	deadlineMs := time.Now().Add(*timeout).UnixMilli()
	resp, err := session.Call(procedure, realm, payload, deadlineMs, id, *timeout)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	if resp.IsError {
		code := resp.Code
		name := resp.Name
		if bc, ok := bolt4.FromU8(code); ok {
			name = bc.Name()
		}
		retryable := bolt4.Code(code).IsRetryable()
		msg := fmt.Sprintf("call failed: %s (code=%d)", name, code)
		if resp.Detail != nil {
			msg += ": " + *resp.Detail
		}
		return report.Fail(*jsonOut, fmt.Errorf("%s", msg), &report.Error{
			Message:   msg,
			Bolt4Code: &code,
			Bolt4Name: name,
			Retryable: &retryable,
		})
	}

	result := callResult{
		Procedure:   procedure,
		RespondedBy: hex.EncodeToString(resp.RespondedBy),
		Payload:     wirevalue.ToJSON(resp.Payload),
		DurationMs:  duration,
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("%s -> %s (%d ms)\n", procedure, result.RespondedBy, duration)
		fmt.Printf("  %v\n", result.Payload)
	})
	return 0
}
