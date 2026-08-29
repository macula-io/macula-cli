package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"github.com/macula-io/macula-go-sdk/cbor"
	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/frame"
	"github.com/macula-io/macula-go-sdk/identity"
	"github.com/macula-io/macula-go-sdk/stream"
	"github.com/macula-io/macula-go-sdk/transport"

	"github.com/macula-io/macula-cli/internal/report"
)

func runStream(args []string) int {
	if len(args) == 0 || args[0] != "probe" {
		fmt.Println("Usage: macula-cli stream probe --provider <host[:port]> --caller <host[:port]>")
		return 2
	}
	return runStreamProbe(args[1:])
}

type streamProbeResult struct {
	Provider     string `json:"provider"`
	Caller       string `json:"caller"`
	Procedure    string `json:"procedure"`
	OpenRouted   bool   `json:"open_routed"`
	ProviderRecv bool   `json:"provider_received_caller_frame"`
	CallerRecv   bool   `json:"caller_received_provider_frame"`
	DurationMs   int64  `json:"duration_ms"`
}

// runStreamProbe is macula-cli's general-purpose version of the same
// diagnostic macula-go-sdk's own TestLiveCrossStationStreamingRoundTrip
// / TestLiveCrossStationStreamingMultiHop live tests hard-code against
// fixed station pairs (see that repo, 2026-08-29): advertise on the
// provider, open a Bidi stream from the caller against a DIFFERENT
// station, and confirm data actually flows both ways through whatever
// station-to-station relay path the mesh picks — not just that the
// stream opens. This is the check that would have caught the
// signer-stamping relay bug immediately instead of by hand.
func runStreamProbe(args []string) int {
	fs := flag.NewFlagSet("stream probe", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	providerHostArg := fs.String("provider", "", "station the provider connects to (required)")
	callerHostArg := fs.String("caller", "", "station the caller connects to (required)")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	procedureFlag := fs.String("procedure", "", "procedure name to advertise/open (default: a random macula_cli.probe.* name)")
	propagationWait := fs.Duration("propagation-wait", 8*time.Second, "time to let the advertise gossip-propagate to the caller's station before opening")
	acceptTimeout := fs.Duration("accept-timeout", 30*time.Second, "how long the provider waits for the relayed STREAM_OPEN")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout for each side")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli stream probe [flags] --provider <host[:port]> --caller <host[:port]>\n\n"+
			"Opens a Bidi stream from a caller on one station to a provider on ANOTHER\n"+
			"station and confirms data flows both ways through the relay — the same\n"+
			"round trip macula-go-sdk's own multi-hop live tests run, generalized to any\n"+
			"pair.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *providerHostArg == "" || *callerHostArg == "" {
		fs.Usage()
		return 2
	}

	providerHost, providerPort, err := parseHostPort(*providerHostArg)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	callerHost, callerPort, err := parseHostPort(*callerHostArg)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	realm, err := parseRealm(*realmHex)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	procedure := *procedureFlag
	if procedure == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		procedure = "macula_cli.probe." + hex.EncodeToString(b)
	}

	// Fresh, ephemeral (never persisted) identities for both roles: a
	// probe simulates two distinct peers, not the operator's own
	// standing identity, so reusing --identity here wouldn't mean
	// anything and grinding a real puzzle twice per probe is cheap.
	providerID, err := identity.Generate()
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("generate provider identity: %w", err), nil)
	}
	callerID, err := identity.Generate()
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("generate caller identity: %w", err), nil)
	}

	start := time.Now()
	connectCtx, connectCancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer connectCancel()
	providerSession, err := connection.Connect(connectCtx, providerHost, providerPort, transport.WebPKI{}, providerID)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("provider connect to %s: %w", *providerHostArg, err), nil)
	}
	defer providerSession.Close("normal", nil, providerID)
	callerSession, err := connection.Connect(connectCtx, callerHost, callerPort, transport.WebPKI{}, callerID)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("caller connect to %s: %w", *callerHostArg, err), nil)
	}
	defer callerSession.Close("normal", nil, callerID)

	advertiseSpec := frame.NewAdvertiseSpec(realm, procedure, providerID.NodeID())
	if err := providerSession.Advertise(advertiseSpec, providerID); err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("advertise on %s: %w", *providerHostArg, err), nil)
	}
	time.Sleep(*propagationWait)

	type acceptResult struct {
		handle *stream.Handle
		err    error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		h, _, err := stream.Accept(providerSession, *acceptTimeout)
		acceptCh <- acceptResult{handle: h, err: err}
	}()

	openCtx, openCancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer openCancel()
	callerHandle, err := stream.Open(openCtx, callerSession, procedure, realm, frame.Bidi,
		cbor.Null(), time.Now().Add(*connectTimeout).UnixMilli(), callerID)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("caller open: %w", err), nil)
	}

	result := streamProbeResult{Provider: *providerHostArg, Caller: *callerHostArg, Procedure: procedure}

	accepted := <-acceptCh
	if accepted.err != nil {
		result.DurationMs = time.Since(start).Milliseconds()
		return report.Fail(*jsonOut, fmt.Errorf("provider never saw the relayed STREAM_OPEN: %w", accepted.err), nil)
	}
	result.OpenRouted = true
	providerHandle := accepted.handle

	if err := callerHandle.SendData(frame.Raw, cbor.Bytes([]byte("macula-cli stream probe: caller->provider")), callerID); err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("caller send: %w", err), nil)
	}
	if err := providerHandle.SendData(frame.Raw, cbor.Bytes([]byte("macula-cli stream probe: provider->caller")), providerID); err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("provider send: %w", err), nil)
	}

	if item, err := providerHandle.Recv(5 * time.Second); err == nil && !item.IsEOF {
		result.ProviderRecv = true
	}
	if item, err := callerHandle.Recv(5 * time.Second); err == nil && !item.IsEOF {
		result.CallerRecv = true
	}

	_ = callerHandle.CloseSend(callerID)
	_ = providerHandle.CloseSend(providerID)
	result.DurationMs = time.Since(start).Milliseconds()

	if !result.ProviderRecv || !result.CallerRecv {
		return report.Fail(*jsonOut, fmt.Errorf(
			"stream opened but data did not fully round-trip (provider_received=%v caller_received=%v) — this is the exact shape of the 2026-08-29 cross-station relay bug",
			result.ProviderRecv, result.CallerRecv), nil)
	}

	report.Ok(*jsonOut, result, func() {
		fmt.Printf("%s <-> %s via %q (%d ms)\n", *providerHostArg, *callerHostArg, procedure, result.DurationMs)
		fmt.Printf("  open routed: %v\n", result.OpenRouted)
		fmt.Printf("  provider received caller's frame: %v\n", result.ProviderRecv)
		fmt.Printf("  caller received provider's frame: %v\n", result.CallerRecv)
	})
	return 0
}
