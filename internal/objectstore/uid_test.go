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
	mustNoErr(t, "open store", err)

	// A never-touched folder reports the initial epoch and first UID.
	uidnext, err := s.UIDNext(mapi.PrivateFIDInbox)
	mustNoErr(t, "uidnext", err)
	wantEq(t, "initial UIDNEXT", uidnext, uint32(1))
	validity, err := s.UIDValidity(mapi.PrivateFIDInbox)
	mustNoErr(t, "uidvalidity", err)
	wantNotEq(t, "UIDVALIDITY epoch", validity, uint32(0))

	// Allocations are monotonic and advance UIDNEXT.
	u1, err := s.AllocUID(mapi.PrivateFIDInbox)
	mustNoErr(t, "alloc uid", err)
	u2, err := s.AllocUID(mapi.PrivateFIDInbox)
	mustNoErr(t, "alloc uid", err)
	wantEq(t, "first allocated UID", u1, uint32(1))
	wantEq(t, "second allocated UID", u2, uint32(2))
	uidnext, _ = s.UIDNext(mapi.PrivateFIDInbox)
	wantEq(t, "UIDNEXT after two allocations", uidnext, uint32(3))

	// UIDVALIDITY is stable across reopen.
	mustNoErr(t, "close store", s.Close())
	s2, err := Open(dir)
	mustNoErr(t, "reopen store", err)
	defer s2.Close()
	v2, _ := s2.UIDValidity(mapi.PrivateFIDInbox)
	wantEq(t, "UIDVALIDITY across reopen", v2, validity)
}
