package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteAtomic proves the ruleset is published atomically: the target ends with
// exactly the written bytes, the temporary file is renamed away (never left behind for
// a reader to mistake for the ruleset), and a second write replaces the first. The
// atomicity matters because the MTA polls this file live, a reader must never observe
// a half-written ruleset.
func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "antispam-rules.cf")

	if err := writeAtomic(path, []byte("first")); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	if got := mustRead(t, path); got != "first" {
		t.Errorf("content = %q, want %q", got, "first")
	}
	// Whatever name the temporary file had, the directory holds only the ruleset
	// afterwards: a leftover would be a second, partial ruleset beside it.
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "antispam-rules.cf" {
		t.Errorf("directory after writeAtomic = %v (%v), want only antispam-rules.cf", entries, err)
	}

	// A second write replaces the contents rather than appending or failing.
	if err := writeAtomic(path, []byte("second")); err != nil {
		t.Fatalf("writeAtomic (overwrite): %v", err)
	}
	if got := mustRead(t, path); got != "second" {
		t.Errorf("after overwrite content = %q, want %q", got, "second")
	}
}

// mustRead returns the file's content and fails the test when it cannot be read,
// so a missing ruleset is reported as missing, not as empty content.
func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
