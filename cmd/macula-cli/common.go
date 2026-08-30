package main

import (
	"encoding/hex"
	"fmt"
	"net"
	"strconv"

	"github.com/macula-io/macula-go-sdk/identity"

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
