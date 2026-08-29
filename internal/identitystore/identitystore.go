// Package identitystore loads or mints the Ed25519 identity macula-cli
// connects with, persisting it to a local file so repeated runs are
// reachable under the same identity rather than a fresh one every time.
package identitystore

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/macula-io/macula-go-sdk/identity"
)

// DefaultPath returns the identity file macula-cli uses when --identity
// isn't given: $XDG_CONFIG_HOME/macula-cli/identity.seed, falling back
// to ~/.config/macula-cli/identity.seed.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("identitystore: resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "macula-cli", "identity.seed"), nil
}

// LoadOrGenerate loads the identity at path, or mints a fresh
// puzzle-hardened one (identity.Generate — never an unhardened
// shortcut, see macula-go-sdk's identity package doc on why that fails
// silently) and persists it if none exists yet. Returns whether a new
// identity was generated, so callers can tell the user their first
// run just took a moment for puzzle grinding.
func LoadOrGenerate(path string) (id identity.KeyPair, generated bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		id, err = identity.Load(path)
		if err != nil {
			return identity.KeyPair{}, false, fmt.Errorf("identitystore: load %s: %w", path, err)
		}
		return id, false, nil
	}

	id, err = identity.Generate()
	if err != nil {
		return identity.KeyPair{}, false, fmt.Errorf("identitystore: generate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return identity.KeyPair{}, false, fmt.Errorf("identitystore: create config dir: %w", err)
	}
	if err := id.Save(path); err != nil {
		return identity.KeyPair{}, false, fmt.Errorf("identitystore: save %s: %w", path, err)
	}
	return id, true, nil
}
