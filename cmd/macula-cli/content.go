package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/content"
	"github.com/macula-io/macula-go-sdk/transport"

	"github.com/macula-io/macula-cli/internal/report"
)

func runContent(args []string) int {
	if len(args) == 0 || args[0] != "probe" {
		fmt.Println("Usage: macula-cli content probe [flags] <host[:port]>")
		return 2
	}
	return runContentProbe(args[1:])
}

type contentProbeResult struct {
	Host         string `json:"host"`
	Mcid         string `json:"mcid"`
	SizeBytes    int    `json:"size_bytes"`
	BytesMatched bool   `json:"bytes_matched"`
	DurationMs   int64  `json:"duration_ms"`
}

// runContentProbe generates random test content, puts it, gets it back,
// and confirms the bytes match — content.Get already Merkle-verifies
// internally and errors on mismatch, so a clean round trip here proves
// both put/get plumbing AND Merkle verification work, without the
// caller needing a pre-existing MCID to test against.
func runContentProbe(args []string) int {
	fs := flag.NewFlagSet("content probe", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	size := fs.Int("size", 4096, "bytes of random test content to round-trip")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli content probe [flags] <host[:port]>\n\n"+
			"Puts N random bytes, gets them back, and confirms the bytes and Merkle\n"+
			"verification both check out.\n\nFlags:\n")
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
	if *size <= 0 {
		return report.Fail(*jsonOut, fmt.Errorf("--size must be positive"), nil)
	}

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer cancel()
	session, err := connection.Connect(ctx, host, port, transport.WebPKI{}, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	data := make([]byte, *size)
	if _, err := rand.Read(data); err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("generate test content: %w", err), nil)
	}

	start := time.Now()
	mcid, err := content.Put(ctx, session, data, "macula-cli-probe", id)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("put: %w", err), nil)
	}
	got, err := content.Get(ctx, session, mcid, id)
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("get (Merkle verification failed or content missing): %w", err), nil)
	}

	result := contentProbeResult{
		Host:         fmt.Sprintf("%s:%d", host, port),
		Mcid:         hex.EncodeToString(mcid[:]),
		SizeBytes:    *size,
		BytesMatched: bytes.Equal(data, got),
		DurationMs:   duration,
	}

	if !result.BytesMatched {
		return report.Fail(*jsonOut, fmt.Errorf("retrieved content did not match what was put (mcid=%s)", result.Mcid), nil)
	}

	report.Ok(*jsonOut, result, func() {
		fmt.Printf("%s: put+get+verify %d bytes OK (%d ms)\n", result.Host, result.SizeBytes, result.DurationMs)
		fmt.Printf("  mcid: %s\n", result.Mcid)
	})
	return 0
}
