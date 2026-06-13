package objectstore

import (
	"path/filepath"
	"testing"

	"hermex/internal/mapi"
)

// TestUIDFacade verifies the UID/UIDVALIDITY facade over the index: an
// untouched folder reports UIDNEXT 1 and a nonzero UIDVALIDITY, allocations are
// monotonic and advance UIDNEXT, and UIDVALIDITY is stable across reopen.
func TestUIDFacade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// A never-touched folder reports the initial epoch and first UID.
	uidnext, err := s.UIDNext(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if uidnext != 1 {
		t.Errorf("initial UIDNEXT = %d, want 1", uidnext)
	}
	validity, err := s.UIDValidity(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if validity == 0 {
		t.Error("UIDVALIDITY is 0; want a nonzero epoch")
	}

	// Allocations are monotonic and advance UIDNEXT.
	u1, err := s.AllocUID(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.AllocUID(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if u1 != 1 || u2 != 2 {
		t.Errorf("allocated UIDs = %d,%d, want 1,2", u1, u2)
	}
	if uidnext, _ = s.UIDNext(mapi.PrivateFIDInbox); uidnext != 3 {
		t.Errorf("UIDNEXT after two allocations = %d, want 3", uidnext)
	}

	// UIDVALIDITY is stable across reopen.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v2, _ := s2.UIDValidity(mapi.PrivateFIDInbox); v2 != validity {
		t.Errorf("UIDVALIDITY changed across reopen: %d -> %d", validity, v2)
	}
}
