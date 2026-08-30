package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/macula-io/macula-go-sdk/cbor"
	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/directdial"
	"github.com/macula-io/macula-go-sdk/frame"
	"github.com/macula-io/macula-go-sdk/transport"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// serveResult doesn't carry the caller's identity or call_id:
// connection.CallHandler's signature is func(cbor.Value) (cbor.Value,
// error) -- payload only, matching macula_station_link.erl's own
// handler contract, which this package's ServeOneCall mirrors exactly.
type serveResult struct {
	Procedure  string `json:"procedure"`
	Payload    any    `json:"payload"`
	Replied    any    `json:"replied"`
	DurationMs int64  `json:"duration_ms"`
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	replyJSON := fs.String("reply", "null", "the RESULT payload to send back, as a JSON document")
	echo := fs.Bool("echo", false, "reply with the caller's own payload instead of -reply")
	timeout := fs.Duration("timeout", 30*time.Second, "connect timeout, plus how long to wait for one inbound CALL")
	direct := fs.Bool("direct", false, "also publish a signed direct-dial DHT advertisement (directdial.AdvertiseDirect), so a caller can resolve and dial this station directly instead of depending on advertise-gossip having propagated a route")
	ttl := fs.Duration("ttl", time.Hour, "direct-dial advertisement TTL (only meaningful with -direct)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli serve [flags] <host[:port]> <procedure>\n\n"+
			"Advertises <procedure>, waits for exactly ONE inbound CALL, answers it,\n"+
			"then exits. The provider-role counterpart to \"call\" -- serves one\n"+
			"request the same way call makes one. Run it in a loop (a shell "+
			"for/while, or your own scripting) for a long-lived server.\n\n"+
			"With -direct, also publishes a DHT direct-dial advertisement -- pair\n"+
			"with \"call -direct\" to reach this station without depending on\n"+
			"ordinary advertise-gossip having propagated a route.\n\nFlags:\n")
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
	replyValue, err := wirevalue.FromJSON([]byte(*replyJSON))
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

	if *direct {
		// AdvertiseDirect calls plain Advertise itself (a station-side
		// registration is required either way -- direct-dial only changes
		// how a caller FINDS the station, not whether the station has a
		// handler registered once it's dialed), so no separate Advertise
		// call is needed here.
		if err := directdial.AdvertiseDirect(session, id, realm, procedure, *ttl); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("advertise (direct): %w", err), nil)
		}
	} else {
		spec := frame.NewAdvertiseSpec(realm, procedure, id.NodeID())
		if err := session.Advertise(spec, id); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("advertise: %w", err), nil)
		}
	}
	defer func() { _ = session.Unadvertise(frame.NewUnadvertiseSpec(realm, procedure, id.NodeID()), id) }()

	var received cbor.Value
	lookup := func(gotRealm []byte, gotProcedure string) (connection.CallHandler, bool) {
		if gotProcedure != procedure {
			return nil, false
		}
		return func(payload cbor.Value) (cbor.Value, error) {
			received = payload
			if *echo {
				return payload, nil
			}
			return replyValue, nil
		}, true
	}

	start := time.Now()
	if err := session.ServeOneCall(lookup, id, *timeout); err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	duration := time.Since(start).Milliseconds()

	replied := replyValue
	if *echo {
		replied = received
	}
	result := serveResult{
		Procedure:  procedure,
		Payload:    wirevalue.ToJSON(received),
		Replied:    wirevalue.ToJSON(replied),
		DurationMs: duration,
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("%s served (%d ms)\n", procedure, duration)
		fmt.Printf("  received: %v\n", result.Payload)
		fmt.Printf("  replied:  %v\n", result.Replied)
	})
	return 0
}
