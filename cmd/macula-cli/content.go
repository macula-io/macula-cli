package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/content"
	"github.com/macula-io/macula-go/manifest"
	"github.com/macula-io/macula-go/transport"

	"github.com/macula-io/macula-cli/internal/report"
)

func runContent(args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: macula-cli content probe|put|get [flags] <host[:port]> ...")
		return 2
	}
	switch args[0] {
	case "probe":
		return runContentProbe(args[1:])
	case "put":
		return runContentPut(args[1:])
	case "get":
		return runContentGet(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "macula-cli content: unknown subcommand %q (want probe, put, or get)\n", args[0])
		return 2
	}
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

type contentPutResult struct {
	Host       string `json:"host"`
	Mcid       string `json:"mcid"`
	SizeBytes  int    `json:"size_bytes"`
	DurationMs int64  `json:"duration_ms"`
}

func runContentPut(args []string) int {
	fs := flag.NewFlagSet("content put", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli content put [flags] <host[:port]> <file>\n\n"+
			"Uploads a file's contents to the mesh and prints its MCID (68 hex chars).\n\nFlags:\n")
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
	filePath := fs.Arg(1)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("read %s: %w", filePath, err), nil)
	}

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer cancel()
	session, err := connection.Connect(ctx, host, port, transport.WebPKI{}, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	mcid, err := content.Put(ctx, session, data, filePath, id)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("put: %w", err), nil)
	}

	result := contentPutResult{
		Host:       fmt.Sprintf("%s:%d", host, port),
		Mcid:       hex.EncodeToString(mcid[:]),
		SizeBytes:  len(data),
		DurationMs: time.Since(start).Milliseconds(),
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Println(result.Mcid)
	})
	return 0
}

type contentGetResult struct {
	Host          string `json:"host"`
	Mcid          string `json:"mcid"`
	SizeBytes     int    `json:"size_bytes"`
	Out           string `json:"out,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
}

func runContentGet(args []string) int {
	fs := flag.NewFlagSet("content get", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	out := fs.String("out", "", "write the retrieved bytes to this file (default: print to stdout in human mode, base64 in --json)")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli content get [flags] <host[:port]> <mcid>\n\n"+
			"Downloads and Merkle-verifies content by its 68-hex-char MCID (content.Get\n"+
			"errors on verification failure, so a clean exit means it checked out).\n\nFlags:\n")
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
	mcid, err := parseMcid(fs.Arg(1))
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

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer cancel()
	session, err := connection.Connect(ctx, host, port, transport.WebPKI{}, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	data, err := content.Get(ctx, session, mcid, id)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("get (Merkle verification failed or content missing): %w", err), nil)
	}

	if *out != "" {
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("write %s: %w", *out, err), nil)
		}
	}

	result := contentGetResult{
		Host:       fmt.Sprintf("%s:%d", host, port),
		Mcid:       fs.Arg(1),
		SizeBytes:  len(data),
		Out:        *out,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if *out == "" && *jsonOut {
		result.ContentBase64 = base64.StdEncoding.EncodeToString(data)
	}
	report.Ok(*jsonOut, result, func() {
		if *out != "" {
			fmt.Printf("wrote %d bytes to %s\n", result.SizeBytes, *out)
		} else {
			os.Stdout.Write(data)
		}
	})
	return 0
}

func parseMcid(s string) (manifest.Mcid, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return manifest.Mcid{}, fmt.Errorf("invalid MCID hex: %w", err)
	}
	if len(b) != 34 {
		return manifest.Mcid{}, fmt.Errorf("MCID must be 34 bytes (68 hex chars), got %d bytes", len(b))
	}
	var mcid manifest.Mcid
	copy(mcid[:], b)
	return mcid, nil
}
