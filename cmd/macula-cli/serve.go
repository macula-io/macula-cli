package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/directdial"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/transport"
	"github.com/macula-io/macula-go/ucan"

	"github.com/macula-io/macula-cli/internal/daemon"
	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// serveResult doesn't carry the caller's identity or call_id:
// connection.CallHandler's signature is func(cbor.Value) (cbor.Value,
// error) -- payload only, matching macula_station_link.erl's own
// handler contract, which this package's ServeOneCall mirrors exactly.
//
// Refused is only meaningful with -require-ucan-issuer: ServeOneCallGated
// returns a nil error both when the handler actually ran AND when a call
// was refused by policy before the handler ever ran (an ERROR frame is
// still a successfully-sent reply, from the server's own point of view).
// Without this field, a policy-refused call would be indistinguishable
// in this command's own output from one that was genuinely served and
// replied to -- Payload/Replied below would otherwise show the
// *configured* -reply value even though an "unauthorized" ERROR frame,
// not that payload, is what actually went out over the wire.
type serveResult struct {
	Procedure string `json:"procedure"`
	Refused   bool   `json:"refused,omitempty"`
	Payload   any    `json:"payload,omitempty"`
	Replied   any    `json:"replied,omitempty"`
	// HandlerError is set when -exec's command failed, timed out, or
	// produced invalid JSON -- a normal outcome for -exec (it's a real
	// caller-visible ERROR reply, sent successfully), not a bug, so
	// this is still report.Ok, same reasoning as Refused above. Replied
	// is omitted in this case: there's no reply value to show, only an
	// error.
	HandlerError string `json:"handler_error,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	replyJSON := fs.String("reply", "null", "the RESULT payload to send back, as a JSON document")
	echo := fs.Bool("echo", false, "reply with the caller's own payload instead of -reply")
	execCmd := fs.String("exec", "", "compute the RESULT per call instead of a fixed -reply: run this shell command, writing the call's payload as one JSON document to its stdin and parsing its entire stdout as the reply (empty stdout replies null); a non-zero exit, a timeout, or invalid JSON on stdout all become a normal ERROR reply to the caller, not a crash. Takes precedence over -reply/-echo.")
	execTimeout := fs.Duration("exec-timeout", daemon.DefaultExecTimeout, "with -exec, how long one invocation may run before it's killed and the call fails with a timeout error")
	timeout := fs.Duration("timeout", 30*time.Second, "connect timeout, plus how long to wait for one inbound CALL")
	direct := fs.Bool("direct", false, "also publish a signed direct-dial DHT advertisement (directdial.AdvertiseDirect), so a caller can resolve and dial this station directly instead of depending on advertise-gossip having propagated a route")
	ttl := fs.Duration("ttl", time.Hour, "direct-dial advertisement TTL (only meaningful with -direct)")
	certChainFile := fs.String("cert-chain", "", "PEM file: this service's own cert chain to embed in its direct-dial advertisement (requires -direct; pair with \"call -direct -realm-ca -org\")")
	requireUcanIssuer := fs.String("require-ucan-issuer", "", "hex-encoded 32-byte Ed25519 public key: gate this procedure to callers presenting a UCAN token issued by this key (pair with \"call -ucan\")")
	viaDaemon := fs.Bool("daemon", false, "register with a running \"macula-cli daemon\" instead of serving one call and exiting -- persistent, answers many calls, survives until unregistered or the daemon stops")
	stopServing := fs.Bool("stop", false, "with -daemon, unregister <procedure> instead of registering it")
	socketName := fs.String("socket-name", daemon.DefaultName, "with -daemon, the target daemon instance's -socket-name")
	socketPath := fs.String("socket", "", "with -daemon, control socket path (default: derived from -socket-name)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli serve [flags] <host[:port]> <procedure>\n"+
			"       macula-cli serve -daemon [flags] <procedure>\n\n"+
			"Advertises <procedure>, waits for exactly ONE inbound CALL, answers it,\n"+
			"then exits. The provider-role counterpart to \"call\" -- serves one\n"+
			"request the same way call makes one. Run it in a loop (a shell "+
			"for/while, or your own scripting) for a long-lived server, or use\n"+
			"-daemon (below) instead of hand-rolling that loop.\n\n"+
			"With -direct, also publishes a DHT direct-dial advertisement -- pair\n"+
			"with \"call -direct\" to reach this station without depending on\n"+
			"ordinary advertise-gossip having propagated a route.\n\n"+
			"With -direct plus -cert-chain, embeds a cert chain in that advertisement\n"+
			"for Slice 7c Direction B managed-realm authorization -- pair with\n"+
			"\"call -direct -realm-ca -org\".\n\n"+
			"With -require-ucan-issuer, refuses any CALL that doesn't present a valid\n"+
			"UCAN token from that issuer, before the handler ever runs -- composes\n"+
			"freely with -direct, since gating and advertising are independent.\n\n"+
			"With -exec, the reply is computed per call instead of a fixed -reply:\n"+
			"the given shell command runs once per inbound CALL, receiving the\n"+
			"call's payload as one JSON document on stdin and answering with\n"+
			"whatever JSON it writes to stdout (empty stdout replies null). A\n"+
			"non-zero exit, a timeout (-exec-timeout), or invalid JSON on stdout\n"+
			"all become a normal ERROR reply to the caller, not a command failure.\n"+
			"Cannot be combined with -reply/-echo.\n\n"+
			"With -daemon, this registers <procedure> with an already-running\n"+
			"\"macula-cli daemon start\" instead of dialing the mesh itself, answers\n"+
			"however many calls arrive until -stop unregisters it or the daemon\n"+
			"stops, and takes no <host[:port]> (the daemon already has one). All the\n"+
			"advertise/UCAN flags above still apply.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *certChainFile != "" && !*direct {
		return report.Fail(*jsonOut, fmt.Errorf("-cert-chain requires -direct (cert-chain authorization is a direct-dial-only feature)"), nil)
	}
	if *execCmd != "" && (*echo || *replyJSON != "null") {
		return report.Fail(*jsonOut, fmt.Errorf("-exec cannot be combined with -reply or -echo (it computes the reply itself, per call)"), nil)
	}
	var requiredIssuer []byte
	if *requireUcanIssuer != "" {
		var hexErr error
		requiredIssuer, hexErr = hex.DecodeString(*requireUcanIssuer)
		if hexErr != nil || len(requiredIssuer) != 32 {
			return report.Fail(*jsonOut, fmt.Errorf("-require-ucan-issuer must be a 32-byte Ed25519 public key as 64 hex chars"), nil)
		}
	}

	if *viaDaemon {
		if fs.NArg() != 1 {
			fs.Usage()
			return 2
		}
		return runServeDaemon(serveDaemonArgs{
			jsonOut:           *jsonOut,
			procedure:         fs.Arg(0),
			socketName:        *socketName,
			socketPath:        *socketPath,
			stop:              *stopServing,
			realmHex:          *realmHex,
			direct:            *direct,
			ttl:               *ttl,
			certChainFile:     *certChainFile,
			requireUcanIssuer: *requireUcanIssuer,
			replyJSON:         *replyJSON,
			echo:              *echo,
			execCmd:           *execCmd,
			execTimeout:       *execTimeout,
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
	replyHandler, err := daemon.BuildReplyHandler(json.RawMessage(*replyJSON), *echo, *execCmd, *execTimeout)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("-reply: %w", err), nil)
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

	switch {
	case *direct && *certChainFile != "":
		// AdvertiseDirectWithCertChain calls plain Advertise itself too
		// (same reasoning as AdvertiseDirect below), so no separate
		// Advertise call is needed here either.
		certChainPEM, readErr := os.ReadFile(*certChainFile)
		if readErr != nil {
			return report.Fail(*jsonOut, fmt.Errorf("read %s: %w", *certChainFile, readErr), nil)
		}
		if err := directdial.AdvertiseDirectWithCertChain(session, id, realm, procedure, *ttl, certChainPEM); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("advertise (direct, cert-chain): %w", err), nil)
		}
	case *direct:
		// AdvertiseDirect calls plain Advertise itself (a station-side
		// registration is required either way -- direct-dial only changes
		// how a caller FINDS the station, not whether the station has a
		// handler registered once it's dialed), so no separate Advertise
		// call is needed here.
		if err := directdial.AdvertiseDirect(session, id, realm, procedure, *ttl); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("advertise (direct): %w", err), nil)
		}
	default:
		spec := frame.NewAdvertiseSpec(realm, procedure, id.NodeID())
		if err := session.Advertise(spec, id); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("advertise: %w", err), nil)
		}
	}
	defer func() { _ = session.Unadvertise(frame.NewUnadvertiseSpec(realm, procedure, id.NodeID()), id) }()

	var received, repliedValue cbor.Value
	var handlerErr error
	handlerRan := false
	lookup := func(gotRealm []byte, gotProcedure string) (connection.CallHandler, bool) {
		if gotProcedure != procedure {
			return nil, false
		}
		return func(payload cbor.Value) (cbor.Value, error) {
			handlerRan = true
			received = payload
			reply, err := replyHandler(payload)
			repliedValue = reply
			handlerErr = err
			return reply, err
		}, true
	}

	start := time.Now()
	var serveErr error
	if requiredIssuer != nil {
		policy := func(_ []byte, _ string) ucan.Policy { return ucan.Required(requiredIssuer) }
		serveErr = session.ServeOneCallGated(lookup, policy, id, *timeout)
	} else {
		serveErr = session.ServeOneCall(lookup, id, *timeout)
	}
	if serveErr != nil {
		return report.Fail(*jsonOut, serveErr, nil)
	}
	duration := time.Since(start).Milliseconds()

	if !handlerRan {
		// A refusal is still a "successfully sent a reply" outcome from
		// ServeOneCallGated's own point of view (an ERROR frame went out
		// over the wire), so this is report.Ok, not report.Fail -- but
		// Payload/Replied would be actively misleading here (they'd show
		// the *configured* -reply value, not the "unauthorized" ERROR
		// that's what the caller actually received), so this branch
		// deliberately omits them rather than reuse the served shape.
		result := serveResult{Procedure: procedure, Refused: true, DurationMs: duration}
		report.Ok(*jsonOut, result, func() {
			fmt.Printf("%s refused (%d ms) -- caller did not present a valid UCAN token from the required issuer\n", procedure, duration)
		})
		return 0
	}

	if handlerErr != nil {
		// A handler error (only reachable via -exec -- the static
		// reply/echo path never errors) is still a successfully-sent
		// reply from ServeOneCall's own point of view, exactly like the
		// UCAN-refused case above -- a real ERROR frame went out over
		// the wire, this command didn't fail. Replied is omitted: there
		// is no reply value, only an error.
		result := serveResult{
			Procedure:    procedure,
			Payload:      wirevalue.ToJSON(received),
			HandlerError: handlerErr.Error(),
			DurationMs:   duration,
		}
		report.Ok(*jsonOut, result, func() {
			fmt.Printf("%s served (%d ms) -- handler error, sent to the caller as an ERROR reply: %s\n", procedure, duration, handlerErr)
			fmt.Printf("  received: %v\n", result.Payload)
		})
		return 0
	}

	result := serveResult{
		Procedure:  procedure,
		Payload:    wirevalue.ToJSON(received),
		Replied:    wirevalue.ToJSON(repliedValue),
		DurationMs: duration,
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("%s served (%d ms)\n", procedure, duration)
		fmt.Printf("  received: %v\n", result.Payload)
		fmt.Printf("  replied:  %v\n", result.Replied)
	})
	return 0
}

// serveDaemonArgs carries -daemon mode's already-parsed flags across
// to runServeDaemon -- a plain struct rather than passing nine loose
// parameters, since most of them are just relayed straight into a
// daemon.ServeRegisterParams.
type serveDaemonArgs struct {
	jsonOut           bool
	procedure         string
	socketName        string
	socketPath        string
	stop              bool
	realmHex          string
	direct            bool
	ttl               time.Duration
	certChainFile     string
	requireUcanIssuer string
	replyJSON         string
	echo              bool
	execCmd           string
	execTimeout       time.Duration
}

// runServeDaemon registers (or, with -stop, unregisters) a.procedure
// with an already-running daemon instead of serving it directly --
// the daemon owns the Session and the actual ServeForever loop; this
// just asks it to add or remove one entry in its registry.
func runServeDaemon(a serveDaemonArgs) int {
	sockPath, err := resolveSocketPath(a.socketPath, a.socketName)
	if err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}

	if a.stop {
		var result daemon.ServeUnregisterResult
		params := daemon.ServeUnregisterParams{Procedure: a.procedure, RealmHex: a.realmHex}
		if err := daemon.Do(sockPath, daemon.MethodServeUnregister, params, &result); err != nil {
			return report.Fail(a.jsonOut, err, nil)
		}
		report.Ok(a.jsonOut, result, func() {
			if result.Unregistered {
				fmt.Printf("%s unregistered\n", a.procedure)
			} else {
				fmt.Printf("%s was not registered\n", a.procedure)
			}
		})
		return 0
	}

	var replyRaw json.RawMessage
	if a.replyJSON != "" && a.replyJSON != "null" {
		replyRaw = json.RawMessage(a.replyJSON)
	}
	params := daemon.ServeRegisterParams{
		Procedure:         a.procedure,
		RealmHex:          a.realmHex,
		Reply:             replyRaw,
		Echo:              a.echo,
		Exec:              a.execCmd,
		ExecTimeoutMs:     a.execTimeout.Milliseconds(),
		Direct:            a.direct,
		TTLSeconds:        int64(a.ttl.Seconds()),
		RequireUcanIssuer: a.requireUcanIssuer,
	}
	if a.certChainFile != "" {
		if !a.direct {
			return report.Fail(a.jsonOut, fmt.Errorf("-cert-chain requires -direct (cert-chain authorization is a direct-dial-only feature)"), nil)
		}
		certChainPEM, readErr := os.ReadFile(a.certChainFile)
		if readErr != nil {
			return report.Fail(a.jsonOut, fmt.Errorf("read %s: %w", a.certChainFile, readErr), nil)
		}
		params.CertChainPEM = string(certChainPEM)
	}

	var result daemon.ServeRegisterResult
	if err := daemon.Do(sockPath, daemon.MethodServeRegister, params, &result); err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}
	report.Ok(a.jsonOut, result, func() {
		fmt.Printf("%s registered with the daemon at %s\n", a.procedure, sockPath)
	})
	return 0
}
