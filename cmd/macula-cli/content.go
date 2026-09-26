package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/macula-io/macula-go/manifest"
	"github.com/macula-io/macula-go/pool"

	"github.com/macula-io/macula-cli/internal/report"
)

// parseMcid reads a content id: 100 hex characters.
func parseMcid(text string) (manifest.Mcid, error) {
	var mcid manifest.Mcid
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != len(mcid) {
		return mcid, errors.New("a content id is 100 hex characters (50 bytes)")
	}
	copy(mcid[:], raw)
	return mcid, nil
}

// contentProbeResult is content shared by one node and fetched by another.
type contentProbeResult struct {
	Mcid  string `json:"mcid"`
	Bytes int    `json:"bytes"`
}

// contentProbe shares size random bytes from sharer and fetches them from
// fetcher, checking they arrive whole; then unshares them.
func contentProbe(ctx context.Context, sharer, fetcher *pool.Pool, realm [32]byte, size int) (contentProbeResult, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return contentProbeResult{}, err
	}
	mcid, err := sharer.ShareContent(ctx, realm, data, "macula-cli-probe.bin")
	if err != nil {
		return contentProbeResult{}, fmt.Errorf("share: %w", err)
	}
	defer sharer.UnshareContent(context.Background(), realm, mcid)
	got, err := fetcher.GetContent(ctx, realm, mcid, pool.ContentOptions{})
	if err != nil {
		return contentProbeResult{}, fmt.Errorf("fetch: %w", err)
	}
	if !bytes.Equal(got, data) {
		return contentProbeResult{}, errors.New("the fetched content differs from what was shared")
	}
	return contentProbeResult{Mcid: hex.EncodeToString(mcid[:]), Bytes: size}, nil
}

func runContent(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: macula-cli content share|get|probe ...")
		return 2
	}
	switch args[0] {
	case "share":
		return runContentShare(args[1:])
	case "get":
		return runContentGet(args[1:])
	case "probe":
		return runContentProbe(args[1:])
	}
	fmt.Fprintf(os.Stderr, "macula-cli content: unknown subcommand %q (share, get, probe)\n", args[0])
	return 2
}

func contentFlags(name, usage string, extra func(*flag.FlagSet)) (*flag.FlagSet, *meshFlags) {
	fs := flag.NewFlagSet("content "+name, flag.ContinueOnError)
	m := &meshFlags{}
	m.register(fs, true)
	if extra != nil {
		extra(fs)
	}
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usage)
		fs.PrintDefaults()
	}
	return fs, m
}

func runContentShare(args []string) int {
	fs, m := contentFlags("share", "usage: macula-cli content share -seed host:port@<node_id> -realm <realm> [flags] <file>\n"+
		"       the content is served from this node while it runs (node-served content): stop it and it is gone", nil)
	duration := fs.Duration("for", 0, "share for this long (default: until interrupted)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	p, err := m.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	mcid, err := p.ShareContent(ctx, realm, data, filepath.Base(fs.Arg(0)))
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, map[string]any{"mcid": hex.EncodeToString(mcid[:]), "bytes": len(data)}, func(w io.Writer) {
		fmt.Fprintf(w, "%x\n", mcid)
	})
	if !m.jsonOut {
		fmt.Fprintln(os.Stderr, "sharing; interrupt to stop")
	}
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	<-ctx.Done()
	_ = p.UnshareContent(context.Background(), realm, mcid)
	return 0
}

func runContentGet(args []string) int {
	fs, m := contentFlags("get", "usage: macula-cli content get -seed host:port@<node_id> -realm <realm> [flags] <mcid>", nil)
	out := fs.String("out", "", "write the content to this file (default: stdout)")
	maxBytes := fs.Uint64("max-bytes", 0, "refuse content larger than this (default: macula's 256 MiB)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	mcid, err := parseMcid(fs.Arg(0))
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	ctx := context.Background()
	p, err := m.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	data, err := p.GetContent(ctx, realm, mcid, pool.ContentOptions{MaxBytes: *maxBytes})
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return report.Fail(m.jsonOut, err)
		}
		report.Ok(m.jsonOut, map[string]any{"mcid": fs.Arg(0), "bytes": len(data), "file": *out}, func(w io.Writer) {
			fmt.Fprintf(w, "%d bytes to %s\n", len(data), *out)
		})
		return 0
	}
	if m.jsonOut {
		return report.Usage(true, errors.New("content get -json needs -out: the content is not JSON"))
	}
	_, _ = report.Out.Write(data)
	return 0
}

func runContentProbe(args []string) int {
	fs, m := contentFlags("probe", "usage: macula-cli content probe -seed host:port@<node_id> [-seed ...] -realm <realm> [flags]\n"+
		"       two keys made for the run: a sharer linked to the first seed, a fetcher to the last", nil)
	size := fs.Int("size", 300_000, "bytes to share (over 256 KiB takes the chunked path)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*m.timeout)
	defer cancel()
	m.ephemeral = true
	sharerFlags, fetcherFlags := *m, *m
	sharerFlags.seeds, fetcherFlags.seeds = m.seeds[:1], m.seeds[len(m.seeds)-1:]
	sharer, err := sharerFlags.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer sharer.Close()
	fetcher, err := fetcherFlags.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer fetcher.Close()
	result, err := contentProbe(ctx, sharer, fetcher, realm, *size)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, result, func(w io.Writer) {
		fmt.Fprintf(w, "%d bytes shared and fetched whole: %s\n", result.Bytes, result.Mcid)
	})
	return 0
}
