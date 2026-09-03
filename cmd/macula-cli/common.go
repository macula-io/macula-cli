package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/transport"

	"github.com/macula-io/macula-cli/internal/identitystore"
)

// defaultPort is macula-station's standard QUIC listener port across
// the whole demo fleet (station-*.macula.io:4433).
const defaultPort uint16 = 4433

// parseHostPort splits "host" or "host:port" into (host, port),
// defaulting to defaultPort when no port is given.
func parseHostPort(s string) (string, uint16, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		// No port present at all — net.SplitHostPort's own error for
		// that case is indistinguishable from a real syntax error
		// without string-matching it, so just retry as host-only.
		return s, defaultPort, nil
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	return host, uint16(port), nil
}

// seedFlag accumulates repeated -seed host[:port] fallback stations
// into a []connection.Seed, parsed via parseHostPort so a malformed
// -seed value fails at flag-parse time rather than at dial time --
// the same flag.Value pattern ucan.go's capabilityFlag already
// established for this codebase's other repeatable flag.
type seedFlag []connection.Seed

func (s *seedFlag) String() string {
	return fmt.Sprintf("%v", []connection.Seed(*s))
}

func (s *seedFlag) Set(v string) error {
	host, port, err := parseHostPort(v)
	if err != nil {
		return fmt.Errorf("-seed %q: %w", v, err)
	}
	*s = append(*s, connection.Seed{Host: host, Port: port})
	return nil
}

// resolveSeeds builds the ordered candidate list every direct-dial
// command uses: the positional <host[:port]> first -- so every
// existing single-host invocation is unaffected -- then any -seed
// fallbacks in the order given.
func resolveSeeds(primary string, extra seedFlag) ([]connection.Seed, error) {
	host, port, err := parseHostPort(primary)
	if err != nil {
		return nil, err
	}
	seeds := make([]connection.Seed, 0, 1+len(extra))
	seeds = append(seeds, connection.Seed{Host: host, Port: port})
	return append(seeds, extra...), nil
}

// dialSeeds is the shared multi-seed connect every direct-dial command
// funnels through -- see connection.ConnectSeeds's own doc for the
// fallback semantics (first seed that answers wins; if all fail, the
// error names every seed tried instead of going silent).
func dialSeeds(ctx context.Context, seeds []connection.Seed, id identity.KeyPair) (*connection.Session, error) {
	return connection.ConnectSeeds(ctx, seeds, transport.WebPKI{}, id)
}

// parseRealm decodes a hex-encoded realm, or returns the all-zero
// realm (32 bytes) when s is empty — the default realm every SDK's own
// live tests use against the demo fleet.
func parseRealm(s string) ([]byte, error) {
	if s == "" {
		return make([]byte, 32), nil
	}
	realm, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid --realm hex: %w", err)
	}
	if len(realm) != 32 {
		return nil, fmt.Errorf("--realm must be 32 bytes (64 hex chars), got %d bytes", len(realm))
	}
	return realm, nil
}

// hexNodeID is the one-line hex encoding of id's public node ID, used
// anywhere a command prints or reports "which identity is this".
func hexNodeID(id identity.KeyPair) string {
	return hex.EncodeToString(id.NodeID())
}

// loadIdentity resolves the --identity path (or the default config
// path) and loads or mints the keypair macula-cli connects with.
func loadIdentity(path string) (identity.KeyPair, bool, error) {
	if path == "" {
		var err error
		path, err = identitystore.DefaultPath()
		if err != nil {
			return identity.KeyPair{}, false, err
		}
	}
	return identitystore.LoadOrGenerate(path)
}
