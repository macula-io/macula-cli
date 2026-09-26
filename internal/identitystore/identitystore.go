// Package identitystore loads or creates the node identity key macula-cli
// joins the mesh with: a macula 12 key (ML-DSA-87, or the pq_hybrid composite)
// whose node_id solves the admission puzzle, kept in a file its owner alone
// can read, so repeated runs are the same node.
package identitystore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/profile"
)

// DefaultPath is the key file used when --identity is not given, under
// os.UserConfigDir(): $XDG_CONFIG_HOME (or ~/.config) on Linux, ~/Library/
// Application Support on macOS, %AppData% on Windows. The 10.x CLI kept an
// Ed25519 seed beside it as identity.seed; macula 12 has no use for it.
func DefaultPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("identitystore: resolve the user config directory: %w", err)
	}
	return filepath.Join(base, "macula-cli", "identity.key"), nil
}

// LoadOrCreate loads the key at path, or creates one there when the file does
// not exist, and says whether it created it: a new key solves the admission
// puzzle, which takes a few seconds. A file holding anything but a macula 12
// key of profile p is refused.
func LoadOrCreate(path string, p profile.Profile) (key *identity.NodeKey, created bool, err error) {
	key, err = identity.LoadKey(path, identity.PurposeIdentity, p)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, fmt.Errorf("identitystore: %s is not a macula 12 key of profile %s (a 10.x identity.seed is not one; "+
			"move it aside and a new key is created): %w", path, p, err)
	}
	key, err = identity.GenerateIdentityKey(p, identity.PuzzleDifficulty)
	if err != nil {
		return nil, false, fmt.Errorf("identitystore: generate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("identitystore: create %s: %w", filepath.Dir(path), err)
	}
	if err := key.Save(path); err != nil {
		return nil, false, fmt.Errorf("identitystore: save %s: %w", path, err)
	}
	return key, true, nil
}
