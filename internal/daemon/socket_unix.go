//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// verifySocketDirSafe refuses to use dir unless it is a real
// directory, owned by the current user, and not accessible to anyone
// else -- the actual defense against the attack os.MkdirAll's own
// "already exists, fine" behavior doesn't cover: a shared,
// world-writable os.TempDir() (i.e. /tmp on a typical multi-user
// Linux box without $XDG_RUNTIME_DIR) lets any local user pre-create
// "macula-cli-<uid>" themselves before the real owner's daemon ever
// runs. Without this check, MkdirAll would silently accept that
// attacker-owned directory, and this daemon's control socket -- which
// has no authentication of its own, the same trust model
// ssh-agent/dockerd use -- would end up reachable (or replaceable) by
// whoever planted it.
func verifySocketDirSafe(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("daemon: stat %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("daemon: %s is a symlink -- refusing to use it (possible symlink attack on a shared temp directory)", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("daemon: %s exists and is not a directory -- refusing to use it", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("daemon: could not determine ownership of %s", dir)
	}
	if stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("daemon: %s is owned by uid %d, not this process's uid %d -- refusing a directory this user didn't create (likely a pre-creation attack on a shared temp directory)", dir, stat.Uid, os.Getuid())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon: %s has permissions %04o (group/other-accessible) -- refusing, the control socket's directory must be 0700", dir, info.Mode().Perm())
	}
	return nil
}
