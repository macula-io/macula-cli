package main

import (
	"os"
	"path/filepath"
	"testing"
)

// resolveArgsJSON's three real paths: inline -args (the default), a
// valid -args-file, and the mutual-exclusivity error when both are
// given a non-default value at once. A silent last-one-wins here would
// mean a caller's -args-file payload could be quietly discarded in
// favor of a stale -args default.
func TestResolveArgsJSON_InlineDefault(t *testing.T) {
	got, err := resolveArgsJSON(`{"a":1}`, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"a":1}` {
		t.Fatalf("got %q, want inline value unchanged", got)
	}
}

func TestResolveArgsJSON_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "args.json")
	want := `{"document_id":"doc-1","raw_bytes":"aGVsbG8="}`
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := resolveArgsJSON("null", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArgsJSON_BothGivenIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "args.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := resolveArgsJSON(`{"a":1}`, path)
	if err == nil {
		t.Fatal("expected an error combining -args and -args-file, got nil")
	}
}

func TestResolveArgsJSON_MissingFile(t *testing.T) {
	_, err := resolveArgsJSON("null", filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected an error for a missing -args-file, got nil")
	}
}
