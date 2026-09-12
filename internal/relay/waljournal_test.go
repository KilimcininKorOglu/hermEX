package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openSpoolAt opens a spool in a temporary directory and returns it with the
// path of its write-ahead log.
func openSpoolAt(t *testing.T) (*Spool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path + "-wal"
}

// walSize reports the size of the write-ahead log, or 0 when SQLite has removed
// the file.
func walSize(t *testing.T, wal string) int64 {
	t.Helper()
	fi, err := os.Stat(wal)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat the write-ahead log: %v", err)
	}
	return fi.Size()
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

// TestSpoolBoundsTheWriteAheadLog is half the defect this change fixes. A spool
// row holds the whole message body, so one large message wrote its whole size
// into the write-ahead log, and SQLite never shrinks that file on its own. Seven
// daemons hold the spool open for their whole life, so the sidecar stayed at the
// size of the largest message ever relayed.
func TestSpoolBoundsTheWriteAheadLog(t *testing.T) {
	s, wal := openSpoolAt(t)
	enqueueBig(t, s, 25<<20)

	// Later writes carry the log past a checkpoint, which is where the bound
	// applies.
	for range 20 {
		enqueueBig(t, s, 16)
	}
	if n := walSize(t, wal); n > journalSizeLimit {
		t.Fatalf("the write-ahead log is %d bytes, want at most %d", n, journalSizeLimit)
	}
}

// TestDrainedSpoolEmptiesTheWriteAheadLog is the other half. The bound alone
// leaves the log at its limit, and a daemon then carries that file for its whole
// life even with nothing queued.
func TestDrainedSpoolEmptiesTheWriteAheadLog(t *testing.T) {
	s, wal := openSpoolAt(t)
	enqueueBig(t, s, 25<<20)
	drain(t, s)

	if n := walSize(t, wal); n != 0 {
		t.Fatalf("a drained spool left a %d byte write-ahead log", n)
	}
}

// TestSpoolWithMessagesLeftKeepsItsWriteAheadLog locks the other side. The
// checkpoint must run only on a drained spool, because it is where no reader is
// streaming a message body out of the database.
func TestSpoolWithMessagesLeftKeepsItsWriteAheadLog(t *testing.T) {
	s, wal := openSpoolAt(t)
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
	if walSize(t, wal) == 0 {
		t.Fatal("the write-ahead log was emptied while a message was still queued")
	}
}
