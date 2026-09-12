package objectstore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// corruptPages overwrites the middle of a SQLite file with a fixed byte pattern,
// leaving the header and the first pages intact. That is what a damaged disk sector
// looks like to SQLite: the schema still reads and some table pages do not.
func corruptPages(t *testing.T, path string) {
	t.Helper()
	// #nosec G304 -- a test file path built from t.TempDir
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if fi.Size() < 3*4096 {
		t.Fatalf("%s is %d bytes, too small to damage one page and keep the rest", path, fi.Size())
	}
	junk := make([]byte, 4096)
	for i := range junk {
		junk[i] = 0xA5
	}
	if _, err := f.WriteAt(junk, fi.Size()/2); err != nil {
		t.Fatalf("write junk: %v", err)
	}
}

// checkpointed closes a store so its WAL is folded back into the database file,
// which is what a damaged-file test needs: damage written beside a live WAL would
// be undone by the next checkpoint.
func checkpointed(t *testing.T, s *Store) string {
	t.Helper()
	dir := s.Dir()
	mustNoErr(t, "close the store", s.Close())
	return dir
}

// TestRecoverDatabaseSalvagesReadableRows drives the whole recovery: a database
// with a damaged page is rebuilt, the rows outside the damage survive, the damaged
// file is kept, and the rebuilt one passes SQLite's own integrity check.
func TestRecoverDatabaseSalvagesReadableRows(t *testing.T) {
	s := openSeededStore(t)
	const seeded = 200
	for i := range seeded {
		mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox),
			checkRaw(string(rune('a'+i%26))), time.Unix(1700000000, 0), 0)
	}
	dir := checkpointed(t, s)
	path := filepath.Join(dir, indexDBName)
	corruptPages(t, path)

	report, err := RecoverDatabase(path)
	mustNoErr(t, "recover database", err)

	if report.Backup == "" {
		t.Fatal("the damaged file was not kept")
	}
	if _, err := os.Stat(report.Backup); err != nil {
		t.Errorf("the damaged file is not at %s: %v", report.Backup, err)
	}
	rows := recoveredRows(t, path, "messages")
	if rows == 0 {
		t.Fatalf("recovered no message row of %d", seeded)
	}
	if rows+report.Lost > seeded {
		t.Errorf("recovered %d rows plus %d lost, more than the %d seeded", rows, report.Lost, seeded)
	}

	// The rebuilt file must be a database SQLite accepts, or the recovery moved
	// the damage rather than removing it.
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	mustNoErr(t, "open the rebuilt database", err)
	defer func() { _ = db.Close() }()
	problems, err := integrityProblems(db)
	mustNoErr(t, "integrity check the rebuilt database", err)
	if len(problems) > 0 {
		t.Errorf("the rebuilt database is still damaged: %v", problems)
	}
}

// TestSalvageRangeLosesOnlyTheUnreadableRow pins the narrowing itself against a
// chosen failure, which a damaged file cannot give: the copy refuses any range
// holding rowid 7, and every other row must still be recovered. Without the
// narrowing the first refusal costs the whole table.
func TestSalvageRangeLosesOnlyTheUnreadableRow(t *testing.T) {
	const badRow = 7
	copy := func(lo, hi int64) (int, error) {
		if lo <= badRow && badRow <= hi {
			return 0, errors.New("database disk image is malformed")
		}
		return int(hi - lo + 1), nil
	}
	var tr TableRecovery
	salvageRange(copy, 1, 100, &tr)

	wantEq(t, "rows recovered around the unreadable one", tr.Rows, 99)
	wantEq(t, "rows lost", tr.LostRows, 1)
}

// recoveredRows counts a table's rows in the rebuilt database.
func recoveredRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	mustNoErr(t, "open the rebuilt database", err)
	defer func() { _ = db.Close() }()
	var n int
	// #nosec G202 -- the table name is a test constant
	mustNoErr(t, "count rows", db.QueryRow(`SELECT COUNT(*) FROM `+quoteIdent(table)).Scan(&n))
	return n
}

// TestRecoverDatabaseKeepsTheSchemaVersion pins what a row-by-row copy silently
// drops: the index database records its schema version in PRAGMA user_version,
// which no table holds. A rebuilt file reporting 0 reads as a downgrade and the
// migration runner then refuses the whole mailbox.
func TestRecoverDatabaseKeepsTheSchemaVersion(t *testing.T) {
	s := openSeededStore(t)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)
	dir := checkpointed(t, s)
	path := filepath.Join(dir, indexDBName)

	before := userVersionOf(t, path)
	if before == 0 {
		t.Fatal("the index database records no user_version, so the assertion below would be vacuous")
	}
	if _, err := RecoverDatabase(path); err != nil {
		t.Fatalf("recover database: %v", err)
	}
	wantEq(t, "user_version after the rebuild", userVersionOf(t, path), before)
}

// userVersionOf reads a database's recorded schema version.
func userVersionOf(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	mustNoErr(t, "open database", err)
	defer func() { _ = db.Close() }()
	var v int
	mustNoErr(t, "read user_version", db.QueryRow(`PRAGMA user_version`).Scan(&v))
	return v
}

// TestRecoverDatabaseLeavesNoStaleJournal pins the state the next open depends
// on: no write-ahead log of the damaged file sits beside the rebuilt one. SQLite
// itself folds and removes the journal when the recovery attaches the damaged
// database, and swapInRebuilt moves any journal that survives that, so this
// asserts the outcome rather than which of the two did it.
func TestRecoverDatabaseLeavesNoStaleJournal(t *testing.T) {
	s := openSeededStore(t)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)
	dir := checkpointed(t, s)
	path := filepath.Join(dir, indexDBName)
	mustNoErr(t, "write a stale journal", os.WriteFile(path+"-wal", []byte("stale"), 0o600))

	if _, err := RecoverDatabase(path); err != nil {
		t.Fatalf("recover database: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		t.Error("a journal of the damaged file is still beside the rebuilt database")
	}
}

// TestRecoveredMailboxOpensAgain is the end-to-end claim a repair has to make: a
// mailbox whose index database was rebuilt opens, serves its folders and lists
// mail. A recovery that produced a file the store refuses would be no recovery.
func TestRecoveredMailboxOpensAgain(t *testing.T) {
	s := openSeededStore(t)
	for i := range 200 {
		mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox),
			checkRaw(string(rune('a'+i%26))), time.Unix(1700000000, 0), 0)
	}
	dir := checkpointed(t, s)
	corruptPages(t, filepath.Join(dir, indexDBName))

	if _, err := RecoverMailboxDatabases(dir, []string{indexDBName}); err != nil {
		t.Fatalf("recover mailbox: %v", err)
	}
	reopened, err := OpenExisting(dir)
	if err != nil {
		t.Fatalf("the recovered mailbox does not open: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := reopened.ListMessages(int64(mapi.PrivateFIDInbox)); err != nil {
		t.Errorf("the recovered mailbox does not list mail: %v", err)
	}
}

// TestRecoverMailboxDatabasesRefusesABusyMailbox pins the precondition: the
// rebuilt file replaces the one a running daemon is reading, so the recovery
// declines while the mailbox is open.
func TestRecoverMailboxDatabasesRefusesABusyMailbox(t *testing.T) {
	s := openSeededStore(t)
	if _, err := RecoverMailboxDatabases(s.Dir(), []string{indexDBName}); err == nil {
		t.Fatal("the recovery ran while the mailbox was open")
	}
}
