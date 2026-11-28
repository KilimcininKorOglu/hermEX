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

	// Another folder's counter exists first, so an allocation that leaked across
	// folders would advance it.
	sentFirst, err := s.AllocUID(mapi.PrivateFIDSentItems)
	mustNoErr(t, "alloc a Sent Items uid", err)
	wantEq(t, "first Sent Items UID", sentFirst, uint32(1))

	// Allocations are monotonic and advance UIDNEXT.
	u1, err := s.AllocUID(mapi.PrivateFIDInbox)
	mustNoErr(t, "alloc uid", err)
	u2, err := s.AllocUID(mapi.PrivateFIDInbox)
	mustNoErr(t, "alloc uid", err)
	wantEq(t, "first allocated UID", u1, uint32(1))
	wantEq(t, "second allocated UID", u2, uint32(2))
	uidnext, err = s.UIDNext(mapi.PrivateFIDInbox)
	mustNoErr(t, "read UIDNEXT", err)
	wantEq(t, "UIDNEXT after two allocations", uidnext, uint32(3))
	sentNext, err := s.UIDNext(mapi.PrivateFIDSentItems)
	mustNoErr(t, "read Sent Items UIDNEXT", err)
	wantEq(t, "Sent Items UIDNEXT after Inbox allocations", sentNext, uint32(2))

	// UIDVALIDITY is stable across reopen.
	mustNoErr(t, "close store", s.Close())
	s2, err := Open(dir)
	mustNoErr(t, "reopen store", err)
	defer mustClose(t, s2)
	v2, err := s2.UIDValidity(mapi.PrivateFIDInbox)
	mustNoErr(t, "read UIDVALIDITY after reopen", err)
	wantEq(t, "UIDVALIDITY across reopen", v2, validity)
	next2, err := s2.UIDNext(mapi.PrivateFIDInbox)
	mustNoErr(t, "read UIDNEXT after reopen", err)
	wantEq(t, "UIDNEXT across reopen", next2, uint32(3))
}
