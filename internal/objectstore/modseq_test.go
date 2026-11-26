package objectstore

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestModSeqAdvancesOnEveryFlag is the load-bearing CONDSTORE invariant: a flag
// change must advance the message's modseq and the folder's HIGHESTMODSEQ, for
// every flag, not only \Seen (whose object-store read_cn already moved). \Flagged
// does not touch the object store's change number, so the IMAP-local modseq is the
// only thing that can record it.
func TestModSeqAdvancesOnEveryFlag(t *testing.T) {
	st := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)

	info := mustAppendMessage(t, st, fid, []byte("Subject: m\r\n\r\nbody"), time.Unix(1, 0), 0)
	before := mustModSeq(t, st, fid, info.UID)
	if before == 0 {
		t.Fatalf("appended message has modseq 0, want a non-zero CONDSTORE modseq")
	}
	hi0 := mustHighestModSeq(t, st, fid)

	// A \Flagged change with no \Seen change must still advance the modseq.
	mustNoErr(t, "set \\Flagged", st.SetMessageFlags(fid, info.UID, FlagFlagged))
	if after := mustModSeq(t, st, fid, info.UID); after <= before {
		t.Errorf("modseq after \\Flagged = %d, want strictly greater than %d", after, before)
	}
	hi1 := mustHighestModSeq(t, st, fid)
	if hi1 <= hi0 {
		t.Errorf("HIGHESTMODSEQ after \\Flagged = %d, want strictly greater than %d", hi1, hi0)
	}

	// A flag store aimed at a UID the folder does not hold changes nothing a
	// CONDSTORE client syncs by (RFC 7162 3.1.3: HIGHESTMODSEQ moves only when a
	// message's metadata changes).
	if err := st.SetMessageFlags(fid, info.UID+100, FlagFlagged); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMessageFlags on a missing UID: err = %v, want ErrNotFound", err)
	}
	wantEq(t, "HIGHESTMODSEQ after a store on a missing UID", mustHighestModSeq(t, st, fid), hi1)
}

// mustModSeq reads one message's CONDSTORE modseq.
func mustModSeq(t *testing.T, st *Store, fid int64, uid uint32) uint64 {
	t.Helper()
	ms, err := st.MessageModSeqs(fid)
	mustNoErr(t, "message modseqs", err)
	return ms[uid]
}

// mustHighestModSeq reads a folder's HIGHESTMODSEQ.
func mustHighestModSeq(t *testing.T, st *Store, fid int64) uint64 {
	t.Helper()
	hi, err := st.FolderHighestModSeq(fid)
	mustNoErr(t, "highest modseq", err)
	return hi
}
