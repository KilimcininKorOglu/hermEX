package store

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Reopening an initialized store must succeed with the schema intact.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
}
