package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/record"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// recordTypes are macula 12's record types by the names the CLI takes.
var recordTypes = map[string]record.Type{
	"node_record":             0x01,
	"procedure_advertisement": 0x06,
	"tombstone":               0x0c,
	"content_announcement":    0x11,
	"station_endpoint":        0x12,
	"org_directory":           0x15,
	"procedure_delegation":    0x16,
}

// foundRecord is a verified DHT record as the CLI reports it.
type foundRecord struct {
	Type      uint64  `json:"type"`
	KeyID     string  `json:"key_id"`
	CreatedAt uint64  `json:"created_at"`
	ExpiresAt uint64  `json:"expires_at"`
	Payload   rawJSON `json:"payload"`
}

// foundRecords is a lookup's verified records, and how many it dropped as
// failing verification.
type foundRecords struct {
	Records []foundRecord `json:"records"`
	Dropped int           `json:"dropped"`
}

func reported(v record.Verified) foundRecord {
	r := v.Record()
	return foundRecord{Type: uint64(r.Type), KeyID: fmt.Sprintf("%x", r.KeyID), CreatedAt: r.CreatedAt,
		ExpiresAt: r.ExpiresAt, Payload: rawJSON(wirevalue.ToJSON(r.Payload))}
}

func reportedAll(vs []record.Verified, dropped int) foundRecords {
	out := foundRecords{Records: make([]foundRecord, 0, len(vs)), Dropped: dropped}
	for _, v := range vs {
		out.Records = append(out.Records, reported(v))
	}
	return out
}

// recordType reads a type by name or number (decimal or 0x hex).
func recordType(text string) (record.Type, error) {
	if t, ok := recordTypes[text]; ok {
		return t, nil
	}
	n, err := strconv.ParseUint(text, 0, 8)
	if err != nil {
		names := make([]string, 0, len(recordTypes))
		for name := range recordTypes {
			names = append(names, name)
		}
		return 0, fmt.Errorf("record type %q: a number or one of %s", text, strings.Join(names, ", "))
	}
	return record.Type(n), nil
}

func findByType(ctx context.Context, p *pool.Pool, t record.Type) (foundRecords, error) {
	vs, dropped, err := p.FindRecordsByType(ctx, t)
	if err != nil {
		return foundRecords{}, err
	}
	return reportedAll(vs, dropped), nil
}

func runDht(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: macula-cli dht find-record|find-records|find-records-by-type ...")
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("dht "+sub, flag.ContinueOnError)
	var m meshFlags
	m.register(fs, false)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: macula-cli dht %s -seed host:port@<node_id> [flags] <%s>\n", sub,
			map[bool]string{true: "type", false: "key hex"}[sub == "find-records-by-type"])
		fs.PrintDefaults()
	}
	if sub != "find-record" && sub != "find-records" && sub != "find-records-by-type" {
		fmt.Fprintf(os.Stderr, "macula-cli dht: unknown subcommand %q\n", sub)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	var (
		key [32]byte
		t   record.Type
		err error
	)
	if sub == "find-records-by-type" {
		t, err = recordType(fs.Arg(0))
	} else {
		key, err = hex32(fs.Arg(0))
	}
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	p, err := m.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	var result foundRecords
	switch sub {
	case "find-record":
		v, err := p.FindRecord(ctx, key)
		if err != nil {
			return report.Fail(m.jsonOut, err)
		}
		result = reportedAll([]record.Verified{v}, 0)
	case "find-records":
		vs, dropped, err := p.FindRecords(ctx, key)
		if err != nil {
			return report.Fail(m.jsonOut, err)
		}
		result = reportedAll(vs, dropped)
	default:
		if result, err = findByType(ctx, p, t); err != nil {
			return report.Fail(m.jsonOut, err)
		}
	}
	report.Ok(m.jsonOut, result, func(w io.Writer) {
		for _, r := range result.Records {
			fmt.Fprintf(w, "type 0x%02x by %s, expires %d: %s\n", r.Type, r.KeyID, r.ExpiresAt, r.Payload)
		}
		fmt.Fprintf(w, "%d records, %d dropped as unverifiable\n", len(result.Records), result.Dropped)
	})
	return 0
}
