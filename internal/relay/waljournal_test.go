package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openSpoolAt opens a spool in a temporary directory and returns it with the
// path of its database file.
func openSpoolAt(t *testing.T) (*Spool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// fileSize reports the size of a spool file, or 0 when SQLite has removed it.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// walSize reports the size of the write-ahead log beside a spool database.
func walSize(t *testing.T, path string) int64 {
	t.Helper()
	return fileSize(t, path+"-wal")
}

// enqueueBig spools one message whose body is size bytes.
func enqueueBig(t *testing.T, s *Spool, size int) {
	t.Helper()
	body := []byte("Subject: big\r\n\r\n" + strings.Repeat("x", size))
	if err := s.Enqueue("a@example.test", []string{"b@example.test"}, body, time.Now()); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

// drain claims every due recipient and settles it as delivered.
func drain(t *testing.T, s *Spool) {
	t.Helper()
	due, err := s.Claim(time.Now(), 100)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, it := range due {
		if err := s.Sent(it.RecipientID); err != nil {
			t.Fatalf("settle: %v", err)
		}
	}
}

// reclaimed is the size both spool files must fall under once the spool is
// drained. A drained spool holds one schema and no row, which measures well
// under this; an unreclaimed file keeps the size of the largest message.
const reclaimed = 1 << 20

// TestSpoolBoundsTheWriteAheadLog is half the defect this change fixes. A spool
// row holds the whole message body, so one large message wrote its whole size
// into the write-ahead log, and SQLite never shrinks that file on its own. Seven
// daemons hold the spool open for their whole life, so the sidecar stayed at the
// size of the largest message ever relayed.
func TestSpoolBoundsTheWriteAheadLog(t *testing.T) {
	s, path := openSpoolAt(t)
	enqueueBig(t, s, 25<<20)

	// Later writes carry the log past a checkpoint, which is where the bound
	// applies.
	for range 20 {
		enqueueBig(t, s, 16)
	}
	if n := walSize(t, path); n > journalSizeLimit {
		t.Fatalf("the write-ahead log is %d bytes, want at most %d", n, journalSizeLimit)
	}
}

// TestDrainedSpoolEmptiesTheWriteAheadLog is the other half. The bound alone
// leaves the log at its limit, and a daemon then carries that file for its whole
// life even with nothing queued.
func TestDrainedSpoolEmptiesTheWriteAheadLog(t *testing.T) {
	s, path := openSpoolAt(t)
	enqueueBig(t, s, 25<<20)
	drain(t, s)

	if n := walSize(t, path); n != 0 {
		t.Fatalf("a drained spool left a %d byte write-ahead log", n)
	}
}

// TestDrainedSpoolCompactsTheDatabase covers the database file. Settling a
// recipient frees its pages but SQLite reuses them in place, so the file kept the
// size of the largest message relayed until a VACUUM rewrote it.
func TestDrainedSpoolCompactsTheDatabase(t *testing.T) {
	s, path := openSpoolAt(t)
	enqueueBig(t, s, 25<<20)
	if n := fileSize(t, path); n < reclaimed {
		t.Fatalf("the spool database is %d bytes, so the message never reached it", n)
	}
	drain(t, s)

	if n := fileSize(t, path); n > reclaimed {
		t.Fatalf("a drained spool left a %d byte database, want at most %d", n, reclaimed)
	}
}

// TestSpoolWithMessagesLeftKeepsItsWriteAheadLog locks the other side. The
// reclamation must run only on a drained spool, because it is where no reader is
// streaming a message body out of the database.
func TestSpoolWithMessagesLeftKeepsItsWriteAheadLog(t *testing.T) {
	s, path := openSpoolAt(t)
	enqueueBig(t, s, 16)
	enqueueBig(t, s, 16)

	due, err := s.Claim(time.Now(), 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("claimed %d recipients, want 1", len(due))
	}
	if err := s.Sent(due[0].RecipientID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if walSize(t, path) == 0 {
		t.Fatal("the write-ahead log was emptied while a message was still queued")
	}
}
