package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// signalRaw is a minimal deliverable message.
func signalRaw(subject string) []byte {
	return []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: " + subject +
		"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody.\r\n")
}

// mustContentSignal reads one side of a folder's content signal.
func mustContentSignal(t *testing.T, s *Store, folderID int64, softDeleted bool) (int, uint64) {
	t.Helper()
	count, marker, err := s.FolderContentSignal(folderID, softDeleted)
	mustNoErr(t, "folder content signal", err)
	return count, marker
}

// TestFolderContentSignalMovesOnEveryContentChange pins what the signal must
// detect. A delivery moves both numbers, a read-state flip moves only the marker
// (the count cannot see it), and a soft delete moves the count down while the
// dumpster side moves up. A caller that remembers the pair therefore sees every
// one of those as a change.
func TestFolderContentSignalMovesOnEveryContentChange(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	count, marker := mustContentSignal(t, s, inbox, false)
	wantEq(t, "empty folder count", count, 0)
	wantEq(t, "empty folder marker", marker, uint64(0))

	info := mustAppendMessage(t, s, inbox, signalRaw("one"), time.Unix(1700000000, 0), 0)
	afterCount, afterMarker := mustContentSignal(t, s, inbox, false)
	wantEq(t, "count after a delivery", afterCount, 1)
	if afterMarker <= marker {
		t.Errorf("marker after a delivery = %d, want above %d", afterMarker, marker)
	}

	mustNoErr(t, "set read state", s.SetMessageReadState(info.ID, true))
	readCount, readMarker := mustContentSignal(t, s, inbox, false)
	wantEq(t, "count after a read flip", readCount, 1)
	if readMarker <= afterMarker {
		t.Errorf("marker after a read flip = %d, want above %d", readMarker, afterMarker)
	}

	// The dumpster is empty until the message is soft-deleted, and holds it after.
	if dumpCount, _ := mustContentSignal(t, s, inbox, true); dumpCount != 0 {
		t.Errorf("dumpster count before the delete = %d, want 0", dumpCount)
	}
	mustNoErr(t, "soft delete", s.SoftDeleteObject(info.ID))
	if liveCount, _ := mustContentSignal(t, s, inbox, false); liveCount != 0 {
		t.Errorf("live count after the delete = %d, want 0", liveCount)
	}
	if dumpCount, _ := mustContentSignal(t, s, inbox, true); dumpCount != 1 {
		t.Errorf("dumpster count after the delete = %d, want 1", dumpCount)
	}
}

// TestFolderChildSignalMovesWithTheChildSet pins the hierarchy half: a child
// created under the folder moves both numbers, and a child created elsewhere moves
// neither, so a hierarchy table is not woken by a folder it does not list.
func TestFolderChildSignalMovesWithTheChildSet(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	count, marker, err := s.FolderChildSignal(inbox)
	mustNoErr(t, "child signal", err)
	wantEq(t, "child count of a leaf folder", count, 0)

	if _, err := s.CreateFolder(&inbox, "Project"); err != nil {
		t.Fatalf("create child folder: %v", err)
	}
	afterCount, afterMarker, err := s.FolderChildSignal(inbox)
	mustNoErr(t, "child signal after a create", err)
	wantEq(t, "child count after a create", afterCount, 1)
	if afterMarker <= marker {
		t.Errorf("marker after a create = %d, want above %d", afterMarker, marker)
	}

	// A folder created at the top level is not the Inbox's child.
	if _, err := s.CreateFolder(nil, "Elsewhere"); err != nil {
		t.Fatalf("create top-level folder: %v", err)
	}
	otherCount, otherMarker, err := s.FolderChildSignal(inbox)
	mustNoErr(t, "child signal after an unrelated create", err)
	wantEq(t, "child count after an unrelated create", otherCount, 1)
	wantEq(t, "marker after an unrelated create", otherMarker, afterMarker)
}
