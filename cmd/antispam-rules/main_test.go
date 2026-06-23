package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteAtomic proves the ruleset is published atomically: the target ends with
// exactly the written bytes, the temporary file is renamed away (never left behind for
// a reader to mistake for the ruleset), and a second write replaces the first. The
// atomicity matters because the MTA polls this file live — a reader must never observe
// a half-written ruleset.
func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "antispam-rules.cf")

	if err := writeAtomic(path, []byte("first")); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Errorf("content = %q, want %q", got, "first")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file %s.tmp lingers after writeAtomic; rename did not clean up", path)
	}

	// A second write replaces the contents rather than appending or failing.
	if err := writeAtomic(path, []byte("second")); err != nil {
		t.Fatalf("writeAtomic (overwrite): %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" {
		t.Errorf("after overwrite content = %q, want %q", got, "second")
	}
}
