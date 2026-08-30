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
// invalid argument") rather than assumed. os.TempDir() is short by
// convention on every platform, so this uses that instead, scoped by
// UID so two different local users sharing a world-writable /tmp don't
// collide (or worse, one intercepting the other's socket) -- os.Getuid
// returns -1 on Windows, which is fine there since %TEMP% is already
// per-user.
func SocketPath(name string) (string, error) {
	if name == "" {
		name = DefaultName
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("macula-cli-%d", os.Getuid()))
	return filepath.Join(dir, name+".sock"), nil
}

// Listen binds the control socket at path. A file already there is
// checked, not trusted: a live daemon answers a dial immediately, a
// stale file left by an unclean exit doesn't -- in which case it's
// removed and rebound rather than left to block every future start.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: create socket directory: %w", err)
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
