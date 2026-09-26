package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"

	"github.com/macula-io/macula-cli/internal/identitystore"
	"github.com/macula-io/macula-cli/internal/report"
)

// defaultPort is a macula station's QUIC port across the fleet.
const defaultPort = 4433

// seedList is the repeatable -seed flag: host:port@<node_id hex>, every seed
// pinned by the node_id its station must prove.
type seedList []pool.Seed

func (s *seedList) String() string { return fmt.Sprint(len(*s), " seeds") }

func (s *seedList) Set(v string) error {
	seed, err := parseSeed(v)
	if err != nil {
		return err
	}
	*s = append(*s, seed)
	return nil
}

// parseSeed reads host[:port]@<node_id hex>; an IPv6 host is bracketed.
func parseSeed(v string) (pool.Seed, error) {
	at := strings.LastIndex(v, "@")
	if at < 0 {
		return pool.Seed{}, fmt.Errorf("-seed %q: want host[:port]@<station node_id hex>; macula 12 pins every seed", v)
	}
	id, err := hex32(v[at+1:])
	if err != nil {
		return pool.Seed{}, fmt.Errorf("-seed %q: the node_id: %w", v, err)
	}
	host, port, err := hostPort(v[:at])
	if err != nil {
		return pool.Seed{}, fmt.Errorf("-seed %q: %w", v, err)
	}
	return pool.Seed{Host: host, Port: port, NodeID: id}, nil
}

func hostPort(v string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(v)
	if err != nil {
		// No port: the whole of v is the host.
		return strings.Trim(v, "[]"), defaultPort, nil
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", 0, fmt.Errorf("the port %q", portText)
	}
	return host, uint16(port), nil
}

func hex32(text string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != 32 {
		return out, errors.New("want 64 hex characters")
	}
	copy(out[:], raw)
	return out, nil
}

// realmID is a realm given as 64 hex, or by its name, whose id is the
// name's sha256.
func realmID(text string) ([32]byte, error) {
	if text == "" {
		return [32]byte{}, errors.New("-realm is required: a realm name (io.macula) or its 64-hex id")
	}
	if id, err := hex32(text); err == nil {
		return id, nil
	}
	return sha256.Sum256([]byte(text)), nil
}

// realmKey is -realm-key: the key as carried, in hex, or @file holding it.
func realmKey(text string) ([]byte, error) {
	if strings.HasPrefix(text, "@") {
		raw, err := os.ReadFile(text[1:])
		if err != nil {
			return nil, fmt.Errorf("-realm-key: %w", err)
		}
		text = string(raw)
	}
	key, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil || len(key) == 0 {
		return nil, errors.New("-realm-key: want the realm key as carried, in hex, or @file")
	}
	return key, nil
}

// meshFlags are the flags every command that joins the mesh takes.
type meshFlags struct {
	jsonOut   bool
	seeds     seedList
	realm     string
	realmKey  string
	identity  string
	profile   string
	ephemeral bool
	timeout   time.Duration
}

func (m *meshFlags) register(fs *flag.FlagSet, withRealm bool) {
	fs.BoolVar(&m.jsonOut, "json", false, "emit a JSON envelope")
	fs.Var(&m.seeds, "seed", "a station: host[:port]@<node_id hex>; repeatable")
	if withRealm {
		fs.StringVar(&m.realm, "realm", "", "the realm: its name (io.macula) or 64-hex id")
		fs.StringVar(&m.realmKey, "realm-key", "", "the realm key as carried, in hex, or @file; none for a node's own namespace")
	}
	fs.StringVar(&m.identity, "identity", "", "the node key file (default: the user config dir's macula-cli/identity.key)")
	fs.StringVar(&m.profile, "profile", "pq_hybrid", "crypto profile: pq_hybrid (the fleet's) or pq_pure")
	fs.BoolVar(&m.ephemeral, "ephemeral", false, "a key made for this run and never saved")
	fs.DurationVar(&m.timeout, "timeout", 30*time.Second, "how long to wait for the first link, and for each call")
}

// key is the node key: made for the run with -ephemeral, else loaded or
// created at -identity.
func (m *meshFlags) key() (*identity.NodeKey, error) {
	p, err := profile.Parse(m.profile)
	if err != nil {
		return nil, err
	}
	if m.ephemeral {
		return identity.GenerateIdentityKey(p, identity.PuzzleDifficulty)
	}
	path := m.identity
	if path == "" {
		if path, err = identitystore.DefaultPath(); err != nil {
			return nil, err
		}
	}
	key, created, err := identitystore.LoadOrCreate(path, p)
	if created && !m.jsonOut {
		fmt.Fprintf(os.Stderr, "created the node key %s\n", path)
	}
	return key, err
}

// realmTrust pins -realm-key for -realm, when both are given.
func (m *meshFlags) realmTrust() (map[[32]byte][]byte, error) {
	if m.realmKey == "" {
		return nil, nil
	}
	id, err := realmID(m.realm)
	if err != nil {
		return nil, err
	}
	key, err := realmKey(m.realmKey)
	if err != nil {
		return nil, err
	}
	return map[[32]byte][]byte{id: key}, nil
}

// connect links key's node to the seeds, returning once one link is up.
func (m *meshFlags) connect(ctx context.Context, key *identity.NodeKey, onLink func(pool.LinkEvent)) (*pool.Pool, error) {
	if len(m.seeds) == 0 {
		return nil, errors.New("-seed is required: host[:port]@<station node_id hex>")
	}
	trust, err := m.realmTrust()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	return pool.Connect(ctx, m.seeds, pool.Opts{IdentityKey: key, RealmTrust: trust, OnLinkEvent: onLink})
}

// join is key() then connect(): the node on the mesh.
func (m *meshFlags) join(ctx context.Context) (*pool.Pool, error) {
	key, err := m.key()
	if err != nil {
		return nil, err
	}
	return m.connect(ctx, key, nil)
}

// ownProcedure expands ~/<name> to <name> in the node's own namespace,
// ~<node_id>/<name>; any other procedure is as given.
func ownProcedure(procedure string, node [32]byte) string {
	if rest, ok := strings.CutPrefix(procedure, "~/"); ok {
		return fmt.Sprintf("~%x/%s", node, rest)
	}
	return procedure
}

// parse parses args into fs and checks the number of positional arguments.
// It returns false with the exit code when the command must stop: 0 after
// -h, and 2 for a malformed invocation, reported as an invalid_argument
// envelope under -json (asked for on the command line or already parsed) and
// as the error and the usage otherwise.
func parse(fs *flag.FlagSet, args []string, jsonOut *bool, arity func(n int) error) (int, bool) {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	fs.SetOutput(os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		fs.Usage()
		return 0, false
	}
	if err == nil && arity != nil {
		err = arity(fs.NArg())
	}
	if err != nil {
		return usageFailure(fs, args, jsonOut, err), false
	}
	return 0, true
}

// usageFailure reports a malformed invocation found after parsing: exit 2.
func usageFailure(fs *flag.FlagSet, args []string, jsonOut *bool, err error) int {
	asJSON := (jsonOut != nil && *jsonOut) || jsonRequested(args)
	code := report.Usage(asJSON, err)
	if !asJSON && fs != nil {
		fs.Usage()
	}
	return code
}

// jsonRequested is whether args ask for -json, read before or without a
// successful parse.
func jsonRequested(args []string) bool {
	for _, a := range args {
		switch a {
		case "-json", "--json", "-json=true", "--json=true":
			return true
		}
	}
	return false
}

// exactly is an arity check for n positional arguments.
func exactly(n int) func(int) error {
	return func(got int) error {
		if got != n {
			return fmt.Errorf("want %d positional arguments, got %d", n, got)
		}
		return nil
	}
}

// unknownSubcommand reports a subcommand that does not exist: exit 2.
func unknownSubcommand(command string, args []string, known string) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	return usageFailure(nil, args, nil, fmt.Errorf("macula-cli %s: unknown subcommand %q (%s)", command, sub, known))
}
