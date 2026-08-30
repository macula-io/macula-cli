package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/transport"

	"github.com/macula-io/macula-cli/internal/report"
)

// connectResult mirrors the three-stage pipeline a real handshake goes
// through, so a caller can tell exactly where a failure happened
// instead of getting one opaque "connect failed." This distinction is
// the whole point of the command: macula-go's own docs record a
// real incident where an unhardened identity made QUIC/TLS look
// perfectly healthy right up until the HELLO silently never arrived,
// and a separate one where an IPv6-only station with no AAAA record on
// its hostname made a plain dial hang with no error at all.
type connectResult struct {
	Host          string   `json:"host"`
	Port          uint16   `json:"port"`
	DNS           dnsStage `json:"dns"`
	QUIC          stage    `json:"quic"`
	Hello         stage    `json:"hello"`
	StationNodeID string   `json:"station_node_id,omitempty"`
	Accepted      *bool    `json:"accepted,omitempty"`
	RefusalCode   *int64   `json:"refusal_code,omitempty"`
}

type dnsStage struct {
	OK         bool     `json:"ok"`
	A          []string `json:"a,omitempty"`
	AAAA       []string `json:"aaaa,omitempty"`
	Error      string   `json:"error,omitempty"`
	DurationMs int64    `json:"duration_ms"`
}

type stage struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

func runConnect(args []string) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	timeout := fs.Duration("timeout", 15*time.Second, "per-stage timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli connect [flags] <host[:port]>\n\n"+
			"Runs the handshake as three separate stages — DNS resolution, raw QUIC/TLS\n"+
			"dial, and the full CONNECT/HELLO handshake — and reports exactly which stage\n"+
			"failed, rather than one opaque error.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	host, port, err := parseHostPort(fs.Arg(0))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	result := connectResult{Host: host, Port: port}

	// Stage 1: DNS.
	dnsStart := time.Now()
	ips, dnsErr := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	result.DNS.DurationMs = time.Since(dnsStart).Milliseconds()
	if dnsErr != nil {
		result.DNS.Error = dnsErr.Error()
		return finishConnect(*jsonOut, result, dnsErr)
	}
	result.DNS.OK = true
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			result.DNS.A = append(result.DNS.A, ip.IP.String())
		} else {
			result.DNS.AAAA = append(result.DNS.AAAA, ip.IP.String())
		}
	}

	// Stage 2: raw QUIC/TLS dial, no macula framing at all yet.
	quicCtx, quicCancel := context.WithTimeout(context.Background(), *timeout)
	defer quicCancel()
	quicStart := time.Now()
	conn, quicErr := transport.Dial(quicCtx, host, port, transport.WebPKI{})
	result.QUIC.DurationMs = time.Since(quicStart).Milliseconds()
	if quicErr != nil {
		result.QUIC.Error = quicErr.Error()
		return finishConnect(*jsonOut, result, quicErr)
	}
	result.QUIC.OK = true
	_ = conn.CloseWithError(0, "macula-cli connect: diagnostic dial complete")

	// Stage 3: the full CONNECT/HELLO handshake, on its own fresh
	// connection (Connect dials internally; reusing the diagnostic one
	// above isn't exposed by the SDK, and re-dialing keeps this
	// command a thin, honest wrapper over the public API).
	id, generated, idErr := loadIdentity(*identityPath)
	if idErr != nil {
		return report.Fail(*jsonOut, idErr, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	helloCtx, helloCancel := context.WithTimeout(context.Background(), *timeout)
	defer helloCancel()
	helloStart := time.Now()
	session, helloErr := connection.Connect(helloCtx, host, port, transport.WebPKI{}, id)
	result.Hello.DurationMs = time.Since(helloStart).Milliseconds()
	if helloErr != nil {
		result.Hello.Error = helloErr.Error()
		return finishConnect(*jsonOut, result, helloErr)
	}
	result.Hello.OK = true
	result.StationNodeID = hex.EncodeToString(session.Station.NodeID)
	accepted := session.Station.Accepted
	result.Accepted = &accepted
	result.RefusalCode = session.Station.RefusalCode
	_ = session.Close("normal", nil, id)

	return finishConnect(*jsonOut, result, nil)
}

func finishConnect(jsonOut bool, result connectResult, err error) int {
	if err != nil {
		return report.Fail(jsonOut, err, nil)
	}
	report.Ok(jsonOut, result, func() {
		fmt.Printf("%s:%d\n", result.Host, result.Port)
		fmt.Printf("  dns    ok  (%d ms) A=%v AAAA=%v\n", result.DNS.DurationMs, result.DNS.A, result.DNS.AAAA)
		fmt.Printf("  quic   ok  (%d ms)\n", result.QUIC.DurationMs)
		fmt.Printf("  hello  ok  (%d ms) station=%s accepted=%v\n",
			result.Hello.DurationMs, result.StationNodeID, *result.Accepted)
	})
	return 0
}
