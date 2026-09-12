package objectstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// checkRaw is a minimal deliverable message.
func checkRaw(subject string) []byte {
	return []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: " + subject +
		"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody.\r\n")
}

// mustCheck runs CheckMailbox over an open store's directory.
func mustCheck(t *testing.T, s *Store) []Finding {
	t.Helper()
	findings, err := CheckMailbox(s.Dir())
	mustNoErr(t, "check mailbox", err)
	return findings
}

// findingOf returns the finding of one kind, reporting whether it is present.
func findingOf(findings []Finding, kind FindingKind) (Finding, bool) {
	for _, f := range findings {
		if f.Kind == kind {
			return f, true
		}
	}
	return Finding{}, false
}

// TestCheckMailboxPassesAHealthyStore pins the baseline the other tests stand on:
// an ordinary mailbox with mail in it reports nothing. A check that flagged a
// healthy store would make every other finding unreadable.
func TestCheckMailboxPassesAHealthyStore(t *testing.T) {
	s := openSeededStore(t)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)

	if findings := mustCheck(t, s); len(findings) != 0 {
		t.Errorf("healthy store reported %v, want nothing", findings)
	}
}

// TestCheckMailboxFindsAnOrphanIndexRow covers the half of the crash gap where the
// object row went and the index row stayed: IMAP then lists a message that cannot
// be opened.
func TestCheckMailboxFindsAnOrphanIndexRow(t *testing.T) {
	s := openSeededStore(t)
	info := mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)

	_, err := s.objdb.Exec(`DELETE FROM messages WHERE message_id=?`, info.ID)
	mustNoErr(t, "drop the object row", err)

	f, ok := findingOf(mustCheck(t, s), FindingOrphanIndexRow)
	if !ok {
		t.Fatalf("no orphan-index-row finding: %v", mustCheck(t, s))
	}
	wantEq(t, "orphan index rows", f.Count, 1)
}

// TestCheckMailboxFindsAMissingIndexRow covers the other half: the object was
// committed and the index row was not, so IMAP and POP3 never see the message.
func TestCheckMailboxFindsAMissingIndexRow(t *testing.T) {
	s := openSeededStore(t)
	info := mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)

	_, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, info.ID)
	mustNoErr(t, "drop the index row", err)

	f, ok := findingOf(mustCheck(t, s), FindingMissingIndexRow)
	if !ok {
		t.Fatalf("no missing-index-row finding: %v", mustCheck(t, s))
	}
	wantEq(t, "missing index rows", f.Count, 1)
}

// TestCheckMailboxIgnoresAnUnindexedObject pins the scope of the missing-index-row
// check: a calendar, contact, task or note object is stored with no index row at
// all, so reporting one as missing would flag every such object in every mailbox.
func TestCheckMailboxIgnoresAnUnindexedObject(t *testing.T) {
	s := openSeededStore(t)
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Appointment")
	props.Set(mapi.PrSubject, "Review")
	if _, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props}); err != nil {
		t.Fatalf("create calendar object: %v", err)
	}

	if f, ok := findingOf(mustCheck(t, s), FindingMissingIndexRow); ok {
		t.Errorf("a calendar object was reported as a missing index row: %v", f)
	}
}

// TestCheckMailboxFindsAMissingContentFile pins the third class: a body or
// attachment is stored in a content file, so a lost file leaves the message listing
// and its body unreadable. Nothing in the databases records the loss.
func TestCheckMailboxFindsAMissingContentFile(t *testing.T) {
	s := openSeededStore(t)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)

	refs, err := referencedContentIDs(s.objdb)
	mustNoErr(t, "referenced content ids", err)
	if len(refs) == 0 {
		t.Fatal("the message offloaded no content, so the assertion below would be vacuous")
	}
	for cid := range refs {
		mustNoErr(t, "remove a content file", os.Remove(cidFilePath(s.Dir(), cid)))
		break
	}

	f, ok := findingOf(mustCheck(t, s), FindingMissingContent)
	if !ok {
		t.Fatalf("no missing-content-file finding: %v", mustCheck(t, s))
	}
	wantEq(t, "missing content files", f.Count, 1)
}

// TestCheckMailboxFindsACorruptDatabase proves the check asks SQLite itself, not
// only its own cross-file comparisons: a database whose pages are overwritten is
// reported, and its rows are not then compared against the healthy file.
func TestCheckMailboxFindsACorruptDatabase(t *testing.T) {
	s := openSeededStore(t)
	dir := s.Dir()
	for i := range 40 {
		mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw(string(rune('a'+i%26))), time.Unix(1700000000, 0), 0)
	}
	mustNoErr(t, "close the store", s.Close())

	corruptPages(t, filepath.Join(dir, indexDBName))

	findings, err := CheckMailbox(dir)
	mustNoErr(t, "check mailbox", err)
	if _, ok := findingOf(findings, FindingCorruptDatabase); !ok {
		if _, ok := findingOf(findings, FindingUnreadableDatabase); !ok {
			t.Fatalf("a damaged database was reported as %v, want corrupt or unreadable", findings)
		}
	}
	if _, ok := findingOf(findings, FindingOrphanIndexRow); ok {
		t.Error("rows of a damaged database were compared against the healthy one")
	}
}

// TestCheckMailboxRefusesAnAbsentStore keeps the maintenance contract every other
// pass follows: a path with no store is reported, never provisioned.
func TestCheckMailboxRefusesAnAbsentStore(t *testing.T) {
	if _, err := CheckMailbox(filepath.Join(t.TempDir(), "nothing")); !errors.Is(err, ErrNotProvisioned) {
		t.Errorf("CheckMailbox on an absent store = %v, want ErrNotProvisioned", err)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "nothing")); err == nil {
		t.Error("CheckMailbox created the directory it was asked about")
	}
}

// TestRepairMailboxReconcilesTheIndex proves the repair closes both halves of the
// crash gap and that the check then passes, which is what an operator reads as
// "fixed".
func TestRepairMailboxReconcilesTheIndex(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	kept := mustAppendMessage(t, s, inbox, checkRaw("kept"), time.Unix(1700000000, 0), 0)
	dropped := mustAppendMessage(t, s, inbox, checkRaw("dropped"), time.Unix(1700000001, 0), 0)

	// One object row goes, one index row goes: one finding of each kind.
	_, err := s.objdb.Exec(`DELETE FROM messages WHERE message_id=?`, dropped.ID)
	mustNoErr(t, "drop the object row", err)
	_, err = s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, kept.ID)
	mustNoErr(t, "drop the index row", err)
	if len(mustCheck(t, s)) != 2 {
		t.Fatalf("setup reported %v, want one finding of each kind", mustCheck(t, s))
	}

	report, err := s.RepairMailbox()
	mustNoErr(t, "repair mailbox", err)
	if report.Folders == 0 {
		t.Error("the repair reconciled no folder")
	}
	if findings := mustCheck(t, s); len(findings) != 0 {
		t.Errorf("after the repair the check still reports %v", findings)
	}
}

// TestRepairMailboxRefusesABusyMailbox pins the precondition: the repair rewrites
// index rows a live reader addresses by UID, so it declines rather than doing it
// while a daemon holds the mailbox.
func TestRepairMailboxRefusesABusyMailbox(t *testing.T) {
	s := openSeededStore(t)
	other, err := Open(s.Dir())
	mustNoErr(t, "second open", err)
	defer func() { _ = other.Close() }()

	if _, err := s.RepairMailbox(); err == nil {
		t.Fatal("the repair ran while the mailbox was open elsewhere")
	} else if !errors.Is(err, ErrMailboxBusy) {
		t.Errorf("repair error = %v, want ErrMailboxBusy", err)
	}
}
