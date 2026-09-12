package objectstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// seedOneMessage provisions a mailbox holding exactly one live inbox message and
// returns its directory and the UID the index gave that message.
//
// Two messages are appended and the first is deleted, so the survivor's UID is 2
// while a rebuilt index would hand it 1. A mailbox seeded with one message would
// get UID 1 either way, and no assertion on it could tell a rebuild from an
// untouched index.
func seedOneMessage(t *testing.T) (dir string, uid uint32) {
	t.Helper()
	dir = t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	inbox := int64(mapi.PrivateFIDInbox)
	first := mustAppendMessage(t, s, inbox, checkRaw("first"), time.Unix(1700000000, 0), 0)
	kept := mustAppendMessage(t, s, inbox, checkRaw("one"), time.Unix(1700000001, 0), 0)
	mustNoErr(t, "delete the first message", s.DeleteMessage(inbox, first.UID))
	mustNoErr(t, "close", s.Close())
	return dir, kept.UID
}

// wantInboxHoldsOne reopens the mailbox and requires the seeded message to be
// listed, which is what IMAP and POP3 serve.
func wantInboxHoldsOne(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	mustNoErr(t, "reopen", err)
	msgs, err := s.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list", err)
	if len(msgs) != 1 {
		_ = s.Close()
		t.Fatalf("inbox holds %d messages, want 1", len(msgs))
	}
	return s
}

// TestOpenRebuildsAnIndexWithALostTable is one half of the defect this change
// fixes. An index that lost a table still passed the schema check, so the mailbox
// opened and then failed on every index read for good. Nothing rebuilt it, and the
// repair command failed with the same error.
func TestOpenRebuildsAnIndexWithALostTable(t *testing.T) {
	dir, _ := seedOneMessage(t)

	db, err := openIndexDirect(dir)
	mustNoErr(t, "open the index directly", err)
	_, err = db.Exec(`DROP TABLE messages`)
	mustNoErr(t, "drop the messages table", err)
	mustNoErr(t, "close the direct handle", db.Close())

	s := wantInboxHoldsOne(t, dir)
	mustNoErr(t, "close", s.Close())
}

// TestOpenRebuildsAnUnreadableIndexFile is the other half: an index file SQLite
// cannot read at all made the whole mailbox refuse to open, even though every
// message was intact in the object store and the index is only a projection of it.
func TestOpenRebuildsAnUnreadableIndexFile(t *testing.T) {
	dir, _ := seedOneMessage(t)

	removeIndexFiles(t, dir)
	mustNoErr(t, "write a non-database", os.WriteFile(
		filepath.Join(dir, indexDBName), []byte("this is not a database"), 0o600))

	s := wantInboxHoldsOne(t, dir)
	mustNoErr(t, "close", s.Close())
}

// TestOpenRebuildsADeletedIndex covers the third shape: the file is gone, so the
// mailbox opened with a fresh empty index and served no mail at all, silently.
func TestOpenRebuildsADeletedIndex(t *testing.T) {
	dir, _ := seedOneMessage(t)
	removeIndexFiles(t, dir)

	s := wantInboxHoldsOne(t, dir)
	mustNoErr(t, "close", s.Close())
}

// TestOpenKeepsAnIntactIndex locks the other side. A rebuild assigns new UIDs and
// forces every IMAP client to resync the mailbox, so an ordinary open must never
// trigger one.
func TestOpenKeepsAnIntactIndex(t *testing.T) {
	dir, uid := seedOneMessage(t)

	s := wantInboxHoldsOne(t, dir)
	defer s.Close()
	msgs, err := s.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list", err)
	if msgs[0].UID != uid {
		t.Fatalf("UID moved from %d to %d, so the index was rebuilt on a healthy mailbox", uid, msgs[0].UID)
	}
}

// TestPermanentDBFailureSpansOnlyUnrecoverableErrors pins the classifier a
// rebuild hangs on. Treating a recoverable failure as permanent would discard an
// intact index, which is worse than the fault it answers.
func TestPermanentDBFailureSpansOnlyUnrecoverableErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	defer s.Close()

	_, missing := s.idxdb.Query(`SELECT 1 FROM no_such_table`)
	if missing == nil {
		t.Fatal("querying a missing table produced no error")
	}
	if !permanentDBFailure(missing) {
		t.Errorf("a missing table is not classed as permanent: %v", missing)
	}
	if permanentDBFailure(errors.New("connection reset")) {
		t.Error("a non-SQLite error is classed as permanent")
	}
	if permanentDBFailure(nil) {
		t.Error("a nil error is classed as permanent")
	}
}
