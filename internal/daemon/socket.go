package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// DefaultName is the socket name used when the user doesn't pick one --
// most setups only ever run one daemon instance.
const DefaultName = "default"

// SocketPath returns the control socket path for a named daemon
// instance. Deliberately NOT under os.UserConfigDir() the way
// identitystore's identity file is: a Unix domain socket path is
// limited to roughly 108 bytes (struct sockaddr_un's sun_path), and a
// config directory can easily exceed that once $HOME or
// $XDG_CONFIG_HOME is a few levels deep -- confirmed directly ("bind:
// invalid argument") rather than assumed.
//
// $XDG_RUNTIME_DIR (systemd-logind's per-UID tmpfs, e.g. /run/user/1000)
// is preferred when set: it's created with mode 0700 and correct
// ownership by logind itself before any session starts, so another
// local user can't write into it at all, let alone pre-create
// "macula-cli" inside it. Falls back to a UID-scoped os.TempDir()
// directory otherwise (no XDG_RUNTIME_DIR: minimal containers, most
// non-systemd setups, macOS -- whose own os.TempDir() is already a
// non-predictable per-user path, unlike Linux's shared /tmp). Either
// way, Listen verifies the directory's actual ownership and
// permissions rather than trusting a bare MkdirAll -- see
// verifySocketDirSafe's own doc on why that trust would be misplaced
// on a shared, world-writable temp directory.
func SocketPath(name string) (string, error) {
	if name == "" {
		name = DefaultName
	}
	return filepath.Join(socketBaseDir(), name+".sock"), nil
}

func socketBaseDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "macula-cli")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("macula-cli-%d", os.Getuid()))
}

// Listen binds the control socket at path. A file already there is
// checked, not trusted: a live daemon answers a dial immediately, a
// stale file left by an unclean exit doesn't -- in which case it's
// removed and rebound rather than left to block every future start.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: create socket directory: %w", err)
	}
	if err := verifySocketDirSafe(dir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		if conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("daemon: %s is already serving -- another daemon instance is running (stop it first, or use a different -socket-name)", path)
		}
		if rmErr := os.Remove(path); rmErr != nil {
			return nil, fmt.Errorf("daemon: remove stale socket %s: %w", path, rmErr)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen on %s: %w", path, err)
	}
	return ln, nil
}
