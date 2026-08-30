//go:build windows

package daemon

// verifySocketDirSafe is a no-op on Windows: %TEMP% is rooted under
// the current user's own profile directory (unlike Linux's shared,
// world-writable /tmp), so the pre-creation attack socket_unix.go
// defends against isn't the same threat here -- another user's
// process doesn't have access to this user's profile tree at all
// under normal ACLs.
func verifySocketDirSafe(dir string) error { return nil }
