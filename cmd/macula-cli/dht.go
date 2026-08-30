package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/dht"
	"github.com/macula-io/macula-go/transport"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// runDht dispatches the read-side of the mesh's signed DHT record store --
// find-record/find-records/find-records-by-type mirror macula.erl's own
// three-function facade (find_record/2, find_records/2,
// find_records_by_type/2) and macula-go's dht.FindRecord/FindRecords/
// FindRecordsByType 1:1. All three always run under the DHT's own
// all-zero realm (dht.dhtRealm, unexported and hardcoded in macula-go --
// there is no -realm flag here, unlike call/pubsub) since DHT storage is
// protocol-internal infrastructure, not a realm-scoped application
// concern; a discovered record's OWN payload (e.g. procedure_advertisement's
// procedure_uri) is what carries the realm a caller would need for the
// capability itself.
//
// put-record is deliberately not exposed here: every current publisher
// (hecate_om_capabilities and its Erlang counterpart) already has its own
// signing/TTL/re-advertise machinery, and a raw put-record subcommand
// would need this CLI to construct and sign records itself with no real
// consumer yet. Add it if/when something needs to publish a record from
// the command line rather than a long-running service.
func runDht(args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: macula-cli dht find-record|find-records|find-records-by-type [flags] <host[:port]> ...")
		return 2
	}
	switch args[0] {
	case "find-record":
		return runDhtFindRecord(args[1:])
	case "find-records":
		return runDhtFindRecords(args[1:])
	case "find-records-by-type":
		return runDhtFindRecordsByType(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "macula-cli dht: unknown subcommand %q (want find-record, find-records, or find-records-by-type)\n", args[0])
		return 2
	}
}

// dhtRecordJSON is the --json shape for one record, common to all three
// subcommands. Payload is the generic wirevalue.ToJSON fallback every
// record type gets; ProcedureAdvertisement is populated in addition
// (not instead) when Type is TypeProcedureAdvertisement, since that's
// the one type this CLI currently has a typed reader for.
type dhtRecordJSON struct {
	Type                   uint8                       `json:"type"`
	TypeName               string                      `json:"type_name,omitempty"`
	KeyHex                 string                      `json:"key"`
	VersionHex             string                      `json:"version"`
	CreatedAtMs            int64                       `json:"created_at_ms"`
	ExpiresAtMs            int64                       `json:"expires_at_ms"`
	Verified               bool                        `json:"verified"`
	VerifyError            string                      `json:"verify_error,omitempty"`
	Payload                any                         `json:"payload"`
	ProcedureAdvertisement *procedureAdvertisementJSON `json:"procedure_advertisement,omitempty"`
}

type procedureAdvertisementJSON struct {
	ProcedureURI   string `json:"procedure_uri"`
	Realm          string `json:"realm,omitempty"`
	Procedure      string `json:"procedure,omitempty"`
	AdvertiserNode string `json:"advertiser_node"`
	ServingStation string `json:"serving_station"`
	HasCertChain   bool   `json:"has_cert_chain"`
}

func recordTypeName(t uint8) string {
	switch t {
	case dht.TypeProcedureAdvertisement:
		return "procedure_advertisement"
	case dht.TypeContentAnnouncement:
		return "content_announcement"
	case dht.TypeStationEndpoint:
		return "station_endpoint"
	default:
		return ""
	}
}

// parseRecordType accepts either a known type name or a raw 0-255
// number, so a caller doesn't need to memorize that procedure_advertisement
// is 6.
func parseRecordType(s string) (uint8, error) {
	switch s {
	case "procedure_advertisement":
		return dht.TypeProcedureAdvertisement, nil
	case "content_announcement":
		return dht.TypeContentAnnouncement, nil
	case "station_endpoint":
		return dht.TypeStationEndpoint, nil
	}
	n, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid record type %q: not a known name (procedure_advertisement, content_announcement, station_endpoint) or a number 0-255: %w", s, err)
	}
	return uint8(n), nil
}

func parseDhtKey(s string) ([32]byte, error) {
	var key [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return key, fmt.Errorf("invalid key hex: %w", err)
	}
	if len(b) != 32 {
		return key, fmt.Errorf("key must be 32 bytes (64 hex chars), got %d bytes", len(b))
	}
	copy(key[:], b)
	return key, nil
}

// splitDiscoveryURI reverses dht.DiscoveryURI's hex(realm) + "/" + procedure
// construction. A realm is always exactly 64 hex chars, so this takes the
// first 64 chars as the realm rather than searching for a slash -- robust
// even if procedure itself happens to contain one.
func splitDiscoveryURI(uri string) (realm, procedure string, ok bool) {
	if len(uri) < 66 || uri[64] != '/' {
		return "", "", false
	}
	return uri[:64], uri[65:], true
}

// toRecordJSON converts one dht.Record into its --json shape, verifying
// its signature along the way (the SDK's own FindRecord/FindRecords/
// FindRecordsByType docs are explicit that a caller must do this before
// trusting the payload -- reported here rather than silently skipped so
// human and --json output both surface an unverifiable or expired record
// instead of presenting it as equally trustworthy as a good one).
func toRecordJSON(rec dht.Record) dhtRecordJSON {
	out := dhtRecordJSON{
		Type:        rec.Type,
		TypeName:    recordTypeName(rec.Type),
		KeyHex:      hex.EncodeToString(rec.Key),
		VersionHex:  hex.EncodeToString(rec.Version),
		CreatedAtMs: rec.CreatedAt,
		ExpiresAtMs: rec.ExpiresAt,
		Payload:     wirevalue.ToJSON(rec.Payload),
	}
	if err := dht.Verify(rec); err != nil {
		out.VerifyError = err.Error()
	} else {
		out.Verified = true
	}
	if rec.Type == dht.TypeProcedureAdvertisement {
		if adv, err := dht.ReadProcedureAdvertisement(rec); err == nil {
			pa := &procedureAdvertisementJSON{
				ProcedureURI:   adv.ProcedureURI,
				AdvertiserNode: hex.EncodeToString(adv.AdvertiserNode),
				ServingStation: hex.EncodeToString(adv.ServingStation),
				HasCertChain:   len(adv.CertChain) > 0,
			}
			if realm, procedure, ok := splitDiscoveryURI(adv.ProcedureURI); ok {
				pa.Realm = realm
				pa.Procedure = procedure
			}
			out.ProcedureAdvertisement = pa
		}
	}
	return out
}

func printRecordHuman(rec dhtRecordJSON) {
	name := rec.TypeName
	if name == "" {
		name = fmt.Sprintf("type=%d", rec.Type)
	}
	verified := "verified"
	if !rec.Verified {
		verified = "UNVERIFIED (" + rec.VerifyError + ")"
	}
	fmt.Printf("- %s  key=%s  %s\n", name, rec.KeyHex, verified)
	if rec.ProcedureAdvertisement != nil {
		pa := rec.ProcedureAdvertisement
		fmt.Printf("    procedure: %s\n", pa.Procedure)
		fmt.Printf("    realm:     %s\n", pa.Realm)
		fmt.Printf("    advertiser_node: %s\n", pa.AdvertiserNode)
		fmt.Printf("    serving_station: %s\n", pa.ServingStation)
	}
}

func connectForDht(ctx context.Context, hostPort string, identityPath string, connectTimeout time.Duration) (*connection.Session, error) {
	host, port, err := parseHostPort(hostPort)
	if err != nil {
		return nil, err
	}
	id, generated, err := loadIdentity(identityPath)
	if err != nil {
		return nil, err
	}
	if generated {
		fmt.Fprintln(os.Stderr, "(generated a new identity — puzzle grinding took a moment)")
	}
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return connection.Connect(cctx, host, port, transport.WebPKI{}, id)
}

type dhtFindRecordResult struct {
	Host   string         `json:"host"`
	Found  bool           `json:"found"`
	Record *dhtRecordJSON `json:"record,omitempty"`
}

func runDhtFindRecord(args []string) int {
	fs := flag.NewFlagSet("dht find-record", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli dht find-record [flags] <host[:port]> <key-hex>\n\n"+
			"Fetches one DHT record by its 32-byte storage key (64 hex chars) -- e.g.\n"+
			"dht.ProcedureKey/StationEndpointKey/ContentKey's output. Always the DHT's\n"+
			"own all-zero realm; there is no -realm flag.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}
	key, err := parseDhtKey(fs.Arg(1))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	ctx := context.Background()
	session, err := connectForDht(ctx, fs.Arg(0), *identityPath, *connectTimeout)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	id, _, _ := loadIdentity(*identityPath)
	defer session.Close("normal", nil, id)

	rec, err := dht.FindRecord(session, id, key)
	if err == dht.ErrNotFound {
		result := dhtFindRecordResult{Host: fs.Arg(0), Found: false}
		report.Ok(*jsonOut, result, func() { fmt.Println("not found") })
		return 0
	}
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	recJSON := toRecordJSON(rec)
	result := dhtFindRecordResult{Host: fs.Arg(0), Found: true, Record: &recJSON}
	report.Ok(*jsonOut, result, func() { printRecordHuman(recJSON) })
	return 0
}

type dhtFindRecordsResult struct {
	Host    string          `json:"host"`
	Count   int             `json:"count"`
	Records []dhtRecordJSON `json:"records"`
}

func runDhtFindRecords(args []string) int {
	fs := flag.NewFlagSet("dht find-records", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli dht find-records [flags] <host[:port]> <key-hex>\n\n"+
			"Fetches EVERY record stored at key -- the full signer-deduped multiset\n"+
			"(e.g. every procedure_advertisement one procedure has from different\n"+
			"providers). Always the DHT's own all-zero realm; there is no -realm flag.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}
	key, err := parseDhtKey(fs.Arg(1))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	ctx := context.Background()
	session, err := connectForDht(ctx, fs.Arg(0), *identityPath, *connectTimeout)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	id, _, _ := loadIdentity(*identityPath)
	defer session.Close("normal", nil, id)

	recs, err := dht.FindRecords(session, id, key)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	recsJSON := make([]dhtRecordJSON, len(recs))
	for i, rec := range recs {
		recsJSON[i] = toRecordJSON(rec)
	}
	result := dhtFindRecordsResult{Host: fs.Arg(0), Count: len(recsJSON), Records: recsJSON}
	report.Ok(*jsonOut, result, func() {
		if len(recsJSON) == 0 {
			fmt.Println("no records at this key")
			return
		}
		for _, r := range recsJSON {
			printRecordHuman(r)
		}
	})
	return 0
}

type dhtFindRecordsByTypeResult struct {
	Host    string          `json:"host"`
	Type    uint8           `json:"type"`
	Count   int             `json:"count"`
	Records []dhtRecordJSON `json:"records"`
}

func runDhtFindRecordsByType(args []string) int {
	fs := flag.NewFlagSet("dht find-records-by-type", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli dht find-records-by-type [flags] <host[:port]> <type>\n\n"+
			"Lists every record of one type currently visible from the station this\n"+
			"connects to -- coverage depends on that station's own view of the DHT,\n"+
			"not the whole mesh. <type> is a known name (procedure_advertisement,\n"+
			"content_announcement, station_endpoint) or a raw number 0-255. This is\n"+
			"the discovery entry point: list procedure_advertisement to see every\n"+
			"capability this station knows about and which realm each is scoped to\n"+
			"(embedded in procedure_uri, decoded into the realm/procedure fields\n"+
			"below). Always the DHT's own all-zero realm; there is no -realm flag.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}
	typ, err := parseRecordType(fs.Arg(1))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	ctx := context.Background()
	session, err := connectForDht(ctx, fs.Arg(0), *identityPath, *connectTimeout)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	id, _, _ := loadIdentity(*identityPath)
	defer session.Close("normal", nil, id)

	recs, err := dht.FindRecordsByType(session, id, typ)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	recsJSON := make([]dhtRecordJSON, len(recs))
	for i, rec := range recs {
		recsJSON[i] = toRecordJSON(rec)
	}
	result := dhtFindRecordsByTypeResult{Host: fs.Arg(0), Type: typ, Count: len(recsJSON), Records: recsJSON}
	report.Ok(*jsonOut, result, func() {
		if len(recsJSON) == 0 {
			fmt.Println("no records of this type visible from this station")
			return
		}
		for _, r := range recsJSON {
			printRecordHuman(r)
		}
	})
	return 0
}
