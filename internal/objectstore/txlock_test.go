package objectstore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// probeName is a named property no store has seen, so a resolver either
// allocates it or reports it as unknown.
func probeName(t *testing.T) mapi.PropertyName {
	t.Helper()
	guid, err := mapi.ParseGUID("00062008-0000-0000-C000-000000000046")
	mustNoErr(t, "parse the namespace", err)
	return mapi.PropertyName{Kind: mapi.MnidID, GUID: guid, LID: 0x4242}
}

// holdWriteLock opens a second handle on the mailbox's object database and
// leaves a write transaction open on it, so the store's own connection has to
// wait for the write lock. It returns the transaction for the caller to end.
func holdWriteLock(t *testing.T, dir string) *sql.Tx {
	t.Helper()
	other, err := sql.Open("sqlite", dsn(filepath.Join(dir, objectsDBName)))
	mustNoErr(t, "open a second handle", err)
	t.Cleanup(func() { _ = other.Close() })

	tx, err := other.Begin()
	mustNoErr(t, "begin on the second handle", err)
	_, err = tx.Exec(
		`INSERT INTO named_properties (propid, name_string) VALUES (?, ?)`,
		int64(0x9000), "GUID=00062008-0000-0000-C000-000000000046,LID=1")
	mustNoErr(t, "write on the second handle", err)
	return tx
}

// TestAConcurrentWriterWaitsForTheLock is the defect this change fixes. The
// connection took its write lock lazily on the first write, and SQLite skips the
// busy handler for that upgrade, so a second writer failed at once with
// SQLITE_BUSY however long busy_timeout was. Here the lock is released well
// inside the timeout, so a writer that waits succeeds.
func TestAConcurrentWriterWaitsForTheLock(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	defer s.Close()

	tx := holdWriteLock(t, dir)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = tx.Rollback()
	}()

	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{probeName(t)})
	mustNoErr(t, "allocate a named property while another writer held the lock", err)
	if ids[0] == 0 {
		t.Fatal("the named property was not allocated")
	}
}

// TestTheNamedPropertyReadPathIgnoresAWriter locks the other side. Taking the
// write lock at BEGIN would make a resolve that only reads queue behind any
// writer for the whole busy timeout, and this resolver is called on nearly every
// message path, so it must not open a transaction at all.
func TestTheNamedPropertyReadPathIgnoresAWriter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	defer s.Close()

	tx := holdWriteLock(t, dir)
	defer tx.Rollback()

	start := time.Now()
	ids, err := s.GetNamedPropIDs(false, []mapi.PropertyName{probeName(t)})
	mustNoErr(t, "resolve a named property while a writer held the lock", err)
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("the read waited %v for the write lock", waited.Round(time.Millisecond))
	}
	if ids[0] != 0 {
		t.Fatalf("an unknown name resolved to %d, want 0", ids[0])
	}
}
