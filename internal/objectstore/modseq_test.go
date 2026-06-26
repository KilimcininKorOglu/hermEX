package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestModSeqAdvancesOnEveryFlag is the load-bearing CONDSTORE invariant: a flag
// change must advance the message's modseq and the folder's HIGHESTMODSEQ — for
// every flag, not only \Seen (whose object-store read_cn already moved). \Flagged
// does not touch the object store's change number, so the IMAP-local modseq is the
// only thing that can record it.
func TestModSeqAdvancesOnEveryFlag(t *testing.T) {
	st := openSeededStore(t)
	fid := int64(mapi.PrivateFIDInbox)

	info, err := st.AppendMessage(fid, []byte("Subject: m\r\n\r\nbody"), time.Unix(1, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	ms0, err := st.MessageModSeqs(fid)
	if err != nil {
		t.Fatal(err)
	}
	before := ms0[info.UID]
	if before == 0 {
		t.Fatalf("appended message has modseq 0, want a non-zero CONDSTORE modseq")
	}
	hi0, err := st.FolderHighestModSeq(fid)
	if err != nil {
		t.Fatal(err)
	}

	// A \Flagged change with no \Seen change must still advance the modseq.
	if err := st.SetMessageFlags(fid, info.UID, FlagFlagged); err != nil {
		t.Fatal(err)
	}
	ms1, err := st.MessageModSeqs(fid)
	if err != nil {
		t.Fatal(err)
	}
	if ms1[info.UID] <= before {
		t.Errorf("modseq after \\Flagged = %d, want strictly greater than %d", ms1[info.UID], before)
	}
	hi1, err := st.FolderHighestModSeq(fid)
	if err != nil {
		t.Fatal(err)
	}
	if hi1 <= hi0 {
		t.Errorf("HIGHESTMODSEQ after \\Flagged = %d, want strictly greater than %d", hi1, hi0)
	}
}
