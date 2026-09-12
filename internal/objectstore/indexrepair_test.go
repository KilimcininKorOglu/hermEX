package objectstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// removeIndexFiles deletes the IMAP index and its journal companions, the shape a
// lost or hand-deleted index takes on disk.
func removeIndexFiles(t *testing.T, dir string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dir, indexDBName+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove index%s: %v", suffix, err)
		}
	}
}

// TestRepairMailboxRebuildsAnEmptiedIndex is the defect this change fixes. A
// mailbox whose IMAP index was lost opens with a fresh empty index, so IMAP and
// POP3 see no mail at all. The repair took its folder list from that same empty
// index, found no folder, and reported success having restored nothing.
func TestRepairMailboxRebuildsAnEmptiedIndex(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)
	mustNoErr(t, "close", s.Close())

	removeIndexFiles(t, dir)

	s2, err := Open(dir)
	mustNoErr(t, "reopen", err)
	defer s2.Close()

	report, err := s2.RepairMailbox()
	mustNoErr(t, "repair", err)
	if report.Folders == 0 {
		t.Fatal("the repair reconciled no folder, so it restored nothing")
	}

	msgs, err := s2.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list", err)
	if len(msgs) != 1 {
		t.Fatalf("inbox holds %d messages after the repair, want 1", len(msgs))
	}
}

// TestRepairMailboxLeavesACalendarFolderUnindexed locks the other half. A
// calendar, contact, task or note object is stored with no index row on purpose,
// so a repair that walked every folder would invent IMAP messages that never
// existed and hand them to every IMAP client.
func TestRepairMailboxLeavesACalendarFolderUnindexed(t *testing.T) {
	s := openSeededStore(t)
	calendar := int64(mapi.PrivateFIDCalendar)
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Appointment")
	props.Set(mapi.PrSubject, "Review")
	_, err := s.CreateMessage(calendar, &oxcmail.Message{Props: props})
	mustNoErr(t, "create a calendar object", err)

	_, err = s.RepairMailbox()
	mustNoErr(t, "repair", err)

	msgs, err := s.ListMessages(calendar)
	mustNoErr(t, "list", err)
	if len(msgs) != 0 {
		t.Fatalf("the calendar folder gained %d IMAP rows, want none", len(msgs))
	}
}
