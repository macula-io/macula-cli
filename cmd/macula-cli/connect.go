package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/macula-io/macula-go/identity"

	"github.com/macula-io/macula-cli/internal/report"
)

// connectResult is the connect diagnostic: the seed's addresses, and the link
// to its station, pinned by node_id, with how long each stage took.
type connectResult struct {
	Node      string   `json:"node"`
	Station   string   `json:"station"`
	Addresses []string `json:"addresses"`
	ResolveMs int64    `json:"resolve_ms"`
	LinkMs    int64    `json:"link_ms"`
	Up        bool     `json:"up"`
}

// connectStages resolves seed's host, then links key's node to it and
// reports the link.
func connectStages(ctx context.Context, m *meshFlags, key *identity.NodeKey) (connectResult, error) {
	seed := m.seeds[0]
	var r connectResult
	r.Station = fmt.Sprintf("%x", seed.NodeID)
	start := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(ctx, seed.Host)
	if err != nil {
		return r, fmt.Errorf("resolve %s: %w", seed.Host, err)
	}
	r.Addresses, r.ResolveMs = addrs, time.Since(start).Milliseconds()
	start = time.Now()
	one := *m
	one.seeds = m.seeds[:1]
	p, err := one.connect(ctx, key, nil)
	if err != nil {
		return r, fmt.Errorf("link to %s:%d as station %x: %w", seed.Host, seed.Port, seed.NodeID[:8], err)
	}
	defer p.Close()
	r.LinkMs = time.Since(start).Milliseconds()
	r.Node = fmt.Sprintf("%x", p.NodeID())
	for _, s := range p.Status() {
		if s.Station == seed.NodeID && s.Up {
			r.Up = true
		}
	}
	return r, nil
}

func runConnect(args []string) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, false)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli connect -seed host:port@<node_id> [flags]")
		fmt.Fprintln(fs.Output(), "       resolves the seed, then links to its station over the macula 12 handshake,")
		fmt.Fprintln(fs.Output(), "       refusing a station that does not prove the node_id")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || len(m.seeds) == 0 {
		fs.Usage()
		return 2
	}
	key, err := m.key()
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	r, err := connectStages(ctx, &m, key)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, r, func(w io.Writer) {
		fmt.Fprintf(w, "resolved %v in %d ms\n", r.Addresses, r.ResolveMs)
		fmt.Fprintf(w, "linked as node %s to station %s in %d ms (up: %v)\n", r.Node, r.Station, r.LinkMs, r.Up)
	})
	return 0
}
