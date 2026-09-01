package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/macula-io/macula-go/bolt4"
	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/directdial"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/transport"

	"github.com/macula-io/macula-cli/internal/daemon"
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
	argsFile := fs.String("args-file", "", "path to a JSON file with the call payload -- for payloads too large for -args' inline string (e.g. hecate-rag.upload_knowledge's raw document bytes); mutually exclusive with a non-default -args")
	timeout := fs.Duration("timeout", 15*time.Second, "connect + call timeout")
	direct := fs.Bool("direct", false, "resolve the procedure's DHT direct-dial advertisement and call its server directly, instead of routing the call through <host>'s own advertise-gossip routes")
	realmCA := fs.String("realm-ca", "", "PEM file: realm CA to verify against for cert-chain-authorized direct-dial (requires -direct and -org)")
	org := fs.String("org", "", "expected org name for cert-chain-authorized direct-dial (requires -direct and -realm-ca)")
	ucanFile := fs.String("ucan", "", "path to a UCAN token file to attach to the call -- NOT composable with -direct: macula-go's direct-dial call path does not currently accept a UCAN token")
	viaDaemon := fs.Bool("via-daemon", false, "route this call through a running \"macula-cli daemon\" instead of dialing the mesh directly -- reuses its already-open Session, takes no <host[:port]>. Not composable with -direct.")
	socketName := fs.String("socket-name", daemon.DefaultName, "with -via-daemon, the target daemon instance's -socket-name")
	socketPath := fs.String("socket", "", "with -via-daemon, control socket path (default: derived from -socket-name)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli call [flags] <host[:port]> <procedure>\n"+
			"       macula-cli call -via-daemon [flags] <procedure>\n\n"+
			"Makes one unary RPC call and prints the RESULT payload, or the ERROR frame's\n"+
			"BOLT#4 code/name if the call failed at the wire level.\n\n"+
			"With -direct, <host> is used only to query the mesh DHT for the procedure's\n"+
			"direct-dial advertisement (published by a provider via AdvertiseDirect); the\n"+
			"actual call dials the resolved serving station in a separate connection.\n"+
			"Fails with \"procedure has no direct-dial advertisement\" if the provider only\n"+
			"advertised the plain (non-direct) way.\n\n"+
			"With -direct plus -realm-ca and -org, only trusts an advertisement whose\n"+
			"embedded cert chain validates to -realm-ca and names -org (Slice 7c Direction\n"+
			"B managed-realm authorization) -- pair with \"serve -direct -cert-chain\".\n\n"+
			"With -ucan, attaches the token at that path to a PLAIN (non-direct) call, for\n"+
			"reaching a procedure served via \"serve -require-ucan-issuer\".\n\n"+
			"With -args-file, reads the call payload from a file instead of -args -- for a\n"+
			"payload too large to pass inline as a command-line argument.\n\n"+
			"With -via-daemon, routes the call through an already-running\n"+
			"\"macula-cli daemon start\" instead of dialing the mesh itself, and takes no\n"+
			"<host[:port]> (the daemon already has one) -- not composable with -direct,\n"+
			"since direct-dial resolves and dials a different station per call.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if (*realmCA == "") != (*org == "") {
		return report.Fail(*jsonOut, fmt.Errorf("-realm-ca and -org must be given together"), nil)
	}
	if *realmCA != "" && !*direct {
		return report.Fail(*jsonOut, fmt.Errorf("-realm-ca/-org require -direct (cert-chain authorization is a direct-dial-only feature)"), nil)
	}
	if *ucanFile != "" && *direct {
		return report.Fail(*jsonOut, fmt.Errorf("-ucan cannot be combined with -direct: macula-go's directdial.Call/CallWithCertChain do not accept a UCAN token today -- attach a UCAN only for a plain call"), nil)
	}
	if *viaDaemon && *direct {
		return report.Fail(*jsonOut, fmt.Errorf("-via-daemon cannot be combined with -direct: the daemon's Session is bound to one already-connected station, direct-dial resolves and dials a different one per call"), nil)
	}
	resolvedArgsJSON, err := resolveArgsJSON(*argsJSON, *argsFile)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	if *viaDaemon {
		if fs.NArg() != 1 {
			fs.Usage()
			return 2
		}
		return runCallViaDaemon(callViaDaemonArgs{
			jsonOut:    *jsonOut,
			procedure:  fs.Arg(0),
			socketName: *socketName,
			socketPath: *socketPath,
			realmHex:   *realmHex,
			argsJSON:   resolvedArgsJSON,
			timeout:    *timeout,
			ucanFile:   *ucanFile,
		})
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
	payload, err := wirevalue.FromJSON([]byte(resolvedArgsJSON))
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
	var resp frame.CallResponse
	switch {
	case *direct && *realmCA != "":
		var realmCAPEM []byte
		realmCAPEM, err = os.ReadFile(*realmCA)
		if err == nil {
			resp, err = directdial.CallWithCertChain(ctx, session, id, realm, procedure, realmCAPEM, *org, payload, *timeout)
		}
	case *direct:
		resp, err = directdial.Call(ctx, session, id, realm, procedure, payload, *timeout)
	case *ucanFile != "":
		var ucanToken []byte
		ucanToken, err = os.ReadFile(*ucanFile)
		if err == nil {
			deadlineMs := time.Now().Add(*timeout).UnixMilli()
			resp, err = session.CallWithUCAN(procedure, realm, payload, deadlineMs, id, *timeout, ucanToken)
		}
	default:
		deadlineMs := time.Now().Add(*timeout).UnixMilli()
		resp, err = session.Call(procedure, realm, payload, deadlineMs, id, *timeout)
	}
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

// resolveArgsJSON returns the call payload as a JSON string, from either
// -args (inline, the common case) or -args-file (a path, for a payload
// too large to pass inline -- e.g. hecate-rag.upload_knowledge's raw
// document bytes). The two are mutually exclusive: passing -args-file
// alongside a non-default -args is a usage error, not a silent
// last-one-wins.
func resolveArgsJSON(argsJSON, argsFile string) (string, error) {
	if argsFile == "" {
		return argsJSON, nil
	}
	if argsJSON != "null" {
		return "", fmt.Errorf("-args-file cannot be combined with -args")
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", argsFile, err)
	}
	return string(data), nil
}

// callViaDaemonArgs carries -via-daemon mode's already-parsed flags
// into runCallViaDaemon, mirroring serveDaemonArgs's own reasoning in
// serve.go for the same shape of split.
type callViaDaemonArgs struct {
	jsonOut    bool
	procedure  string
	socketName string
	socketPath string
	realmHex   string
	argsJSON   string
	timeout    time.Duration
	ucanFile   string
}

// runCallViaDaemon asks a running daemon to make the call on our
// behalf, over its already-open Session, instead of dialing the mesh
// here.
func runCallViaDaemon(a callViaDaemonArgs) int {
	sockPath, err := resolveSocketPath(a.socketPath, a.socketName)
	if err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}

	var payloadRaw json.RawMessage
	if a.argsJSON != "" && a.argsJSON != "null" {
		payloadRaw = json.RawMessage(a.argsJSON)
	}
	params := daemon.CallInvokeParams{
		Procedure: a.procedure,
		RealmHex:  a.realmHex,
		Payload:   payloadRaw,
		TimeoutMs: a.timeout.Milliseconds(),
	}
	if a.ucanFile != "" {
		token, readErr := os.ReadFile(a.ucanFile)
		if readErr != nil {
			return report.Fail(a.jsonOut, fmt.Errorf("read %s: %w", a.ucanFile, readErr), nil)
		}
		params.UcanTokenHex = hex.EncodeToString(token)
	}

	var result daemon.CallInvokeResult
	if err := daemon.Do(sockPath, daemon.MethodCallInvoke, params, &result); err != nil {
		var derr *daemon.DaemonError
		if errors.As(err, &derr) && derr.Bolt4Name != "" {
			return report.Fail(a.jsonOut, err, &report.Error{
				Message:   derr.Message,
				Bolt4Code: derr.Bolt4Code,
				Bolt4Name: derr.Bolt4Name,
				Retryable: derr.Retryable,
			})
		}
		return report.Fail(a.jsonOut, err, nil)
	}

	out := callResult{
		Procedure:   a.procedure,
		RespondedBy: result.RespondedBy,
		Payload:     result.Payload,
		DurationMs:  result.DurationMs,
	}
	report.Ok(a.jsonOut, out, func() {
		fmt.Printf("%s -> %s (%d ms)\n", a.procedure, out.RespondedBy, out.DurationMs)
		fmt.Printf("  %v\n", out.Payload)
	})
	return 0
}
