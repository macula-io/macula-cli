package identitystore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/macula-io/macula-go/profile"
)

func TestAKeyIsCreatedOnceAndLoadsBackAsTheSameNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "identity.key")
	first, created, err := LoadOrCreate(path, profile.PQPure)
	if err != nil || !created {
		t.Fatalf("first: created %v, %v", created, err)
	}
	again, created, err := LoadOrCreate(path, profile.PQPure)
	if err != nil || created {
		t.Fatalf("again: created %v, %v", created, err)
	}
	a, _ := first.NodeID()
	b, _ := again.NodeID()
	if a != b {
		t.Fatalf("loaded %x, created %x", b, a)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("key file mode %v, want owner-only", info.Mode().Perm())
		}
	}
}

func TestAKeyOfTheOtherProfileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.key")
	if _, _, err := LoadOrCreate(path, profile.PQPure); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadOrCreate(path, profile.PQHybrid)
	if err == nil {
		t.Fatal("a pq_pure key loaded as pq_hybrid")
	}
	if strings.Contains(err.Error(), "move it aside") || !strings.Contains(err.Error(), "-profile") {
		t.Fatalf("%v: a key of the other profile is the user's identity; say -profile, never move it aside", err)
	}
}

func TestAKeyFileOthersCanReadIsRefusedNamingTheFix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do not govern key files on Windows")
	}
	path := filepath.Join(t.TempDir(), "identity.key")
	if _, _, err := LoadOrCreate(path, profile.PQPure); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadOrCreate(path, profile.PQPure)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") || strings.Contains(err.Error(), "move it aside") {
		t.Fatalf("%v, want the permission named with its fix", err)
	}
}

func TestAFileThatIsNotAMacula12KeyIsRefusedNamingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.seed")
	if err := os.WriteFile(path, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadOrCreate(path, profile.PQHybrid)
	if err == nil || !strings.Contains(err.Error(), "not a macula 12 key") || !strings.Contains(err.Error(), path) {
		t.Fatalf("%v, want a refusal naming the file as not a macula 12 key", err)
	}
}

func TestTheDefaultPathIsTheNewKeyFile(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "identity.key" || filepath.Base(filepath.Dir(path)) != "macula-cli" {
		t.Fatalf("default path %s", path)
	}
}
