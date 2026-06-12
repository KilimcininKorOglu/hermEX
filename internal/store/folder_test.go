package store

import (
	"path/filepath"
	"testing"
)

// IMAP correctness hinges on UIDs being monotonic and never reused, and on a
// stable UIDVALIDITY — both must hold across a store reopen.
func TestAllocUIDMonotonicAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := s.CreateFolder(nil, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	for want := uint32(1); want <= 3; want++ {
		got, err := s.AllocUID(fid)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("AllocUID = %d, want %d", got, want)
		}
	}
	uv, err := s.UIDValidity(fid)
	if err != nil {
		t.Fatal(err)
	}
	if uv == 0 {
		t.Error("UIDValidity = 0, want nonzero")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.AllocUID(fid)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 {
		t.Errorf("AllocUID after reopen = %d, want 4 (no reuse)", got)
	}
	uv2, err := s2.UIDValidity(fid)
	if err != nil {
		t.Fatal(err)
	}
	if uv2 != uv {
		t.Errorf("UIDVALIDITY changed across reopen: %d -> %d", uv, uv2)
	}
}
