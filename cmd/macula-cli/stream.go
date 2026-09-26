package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/stationlink"

	"github.com/macula-io/macula-cli/internal/report"
)

// streamProbeResult is a streaming round trip: chunks sent to a provider on
// one station and echoed back through another.
type streamProbeResult struct {
	Procedure string `json:"procedure"`
	Chunks    int    `json:"chunks"`
	Bytes     int    `json:"bytes"`
	RoundMs   int64  `json:"round_trip_ms"`
}

// echoStream echoes every chunk until the caller ends its sending.
func echoStream(ctx context.Context, s *stationlink.Stream) error {
	for {
		e, err := s.Recv(ctx)
		if err != nil {
			return err
		}
		switch e.Kind {
		case stationlink.StreamData:
			body, _ := e.Body.AsBytes()
			if err := s.Send(body); err != nil {
				return err
			}
		case stationlink.StreamEnd:
			return nil
		}
	}
}

// streamProbe serves a bidi echo in provider's own namespace, opens it from
// caller, sends chunks of size bytes, and checks each comes back unchanged.
func streamProbe(ctx context.Context, provider, caller *pool.Pool, realm [32]byte, chunks, size int) (streamProbeResult, error) {
	procedure := ownProcedure("~/stream_probe", provider.NodeID())
	served, err := provider.Serve(ctx, pool.Offer{Realm: realm, Procedure: procedure,
		Stream: &stationlink.StreamOffer{Mode: frame.Bidi, Handler: echoStream}})
	if err != nil {
		return streamProbeResult{}, fmt.Errorf("serve the probe: %w", err)
	}
	defer served.Stop()
	start := time.Now()
	s, err := caller.OpenStream(ctx, pool.StreamCall{Realm: realm, Procedure: procedure, Provider: provider.NodeID(), Mode: frame.Bidi})
	if err != nil {
		return streamProbeResult{}, fmt.Errorf("open the probe: %w", err)
	}
	defer s.Close()
	for i := 0; i < chunks; i++ {
		chunk := bytes.Repeat([]byte{byte(i)}, size)
		if err := s.Send(chunk); err != nil {
			return streamProbeResult{}, err
		}
		e, err := s.Recv(ctx)
		if err != nil {
			return streamProbeResult{}, err
		}
		body, _ := e.Body.AsBytes()
		if e.Kind != stationlink.StreamData || !bytes.Equal(body, chunk) {
			return streamProbeResult{}, fmt.Errorf("chunk %d came back altered", i)
		}
	}
	if err := s.CloseSend(); err != nil {
		return streamProbeResult{}, err
	}
	return streamProbeResult{Procedure: procedure, Chunks: chunks, Bytes: chunks * size,
		RoundMs: time.Since(start).Milliseconds()}, nil
}

func runStream(args []string) int {
	if len(args) == 0 || args[0] != "probe" {
		fmt.Fprintln(os.Stderr, "usage: macula-cli stream probe -seed host:port@<node_id> [-seed ...] -realm <realm> [flags]")
		return 2
	}
	fs := flag.NewFlagSet("stream probe", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	chunks := fs.Int("chunks", 8, "chunks to send")
	size := fs.Int("size", 1024, "bytes per chunk")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli stream probe -seed host:port@<node_id> [-seed ...] -realm <realm> [flags]")
		fmt.Fprintln(fs.Output(), "       two keys made for the run: a provider linked to the first seed, a caller to the last")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if *chunks < 1 || *size < 1 {
		return report.Usage(m.jsonOut, errors.New("-chunks and -size are at least 1"))
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*m.timeout)
	defer cancel()
	m.ephemeral = true
	providerFlags, callerFlags := m, m
	providerFlags.seeds, callerFlags.seeds = m.seeds[:1], m.seeds[len(m.seeds)-1:]
	provider, err := providerFlags.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer provider.Close()
	caller, err := callerFlags.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer caller.Close()
	result, err := streamProbe(ctx, provider, caller, realm, *chunks, *size)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, result, func(w io.Writer) {
		fmt.Fprintf(w, "%d chunks, %d bytes, round trip in %d ms\n", result.Chunks, result.Bytes, result.RoundMs)
	})
	return 0
}
