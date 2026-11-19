package objectstore

import (
	"testing"
	"time"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// wideGiven is a given set naming every home-replica id up to 2^47, the kind of
// range a client can put in one GLOBSET Range command ([MS-OXCFXICS] 2.2.2.6.3).
const wideTop = uint64(1) << 47

func wideGiven() *ics.IDSet {
	s := ics.NewIDSet(ics.FormIDLoose, nil)
	s.AppendRange(homeReplID, 1, wideTop)
	return s
}

// withinDeadline runs fn and fails the test when it has not returned after two
// seconds: work that walks a given range value by value never returns here.
func withinDeadline(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sync diff was still running after 2s; it walks the given range one id at a time")
	}
}

// checkDeletedRanges asserts the deleted ranges cover exactly [1, wideTop] minus
// the ids kept: every given id the store does not hold is reported deleted, and
// no id the store still holds is.
func checkDeletedRanges(t *testing.T, got []IDRange, kept ...uint64) {
	t.Helper()
	var total uint64
	for _, r := range got {
		if r.Lo > r.Hi || r.Lo < 1 || r.Hi > wideTop {
			t.Fatalf("deleted range %+v lies outside the given range", r)
		}
		for _, k := range kept {
			if k >= r.Lo && k <= r.Hi {
				t.Errorf("deleted range %+v names stored id %d", r, k)
			}
		}
		total += r.Hi - r.Lo + 1
	}
	if want := wideTop - uint64(len(kept)); total != want {
		t.Errorf("the deleted ranges name %d ids, want %d", total, want)
	}
}

// TestContentSyncHandlesAWideGivenRange sends a given set covering 2^47 ids
// against a folder holding one normal and one associated message. The diff must
// report every other id deleted without visiting each one, keep the normal
// message, and report the associated one as no longer in scope.
func TestContentSyncHandlesAWideGivenRange(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	normal, err := s.CreateMessage(fld, contactMsg("normal"))
	mustNoErr(t, "create the normal message", err)
	assoc, err := s.CreateMessage(fld, contactMsg("assoc"))
	mustNoErr(t, "create the associated message", err)
	_, err = s.objdb.Exec(`UPDATE messages SET is_associated=1 WHERE message_id=?`, assoc)
	mustNoErr(t, "mark the message associated", err)

	var res ContentSyncResult
	withinDeadline(t, func() {
		res, err = s.GetContentSync(ContentSyncRequest{FolderID: fld, Given: wideGiven(), Seen: looseSet()})
	})
	mustNoErr(t, "GetContentSync", err)
	checkDeletedRanges(t, res.DeletedMIDs, uint64(normal), uint64(assoc))
	eqSet(t, "NoLongerMIDs", res.NoLongerMIDs, uint64(assoc))
}

// TestHierarchySyncHandlesAWideGivenRange is the same check on the folder
// hierarchy diff, which has no out-of-scope class.
func TestHierarchySyncHandlesAWideGivenRange(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "wide-root")
	mustNoErr(t, "create the root", err)
	child, err := s.CreateFolder(&parent, "A")
	mustNoErr(t, "create the child", err)

	var res HierarchySyncResult
	withinDeadline(t, func() {
		res, err = s.GetHierarchySync(HierarchySyncRequest{FolderID: parent, Given: wideGiven()})
	})
	mustNoErr(t, "GetHierarchySync", err)
	checkDeletedRanges(t, res.DeletedFIDs, uint64(child))
}
