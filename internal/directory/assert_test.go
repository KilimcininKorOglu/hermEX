package directory

import (
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"hermex/internal/logging"
)

// The directory tests are provisioning-heavy: nearly every one opens the test
// database, ensures the schema, clears the tables, then creates a domain and a
// couple of users before it asserts anything. Written out, that setup and its
// error handling is most of the test body, and each assertion after it is
// another hand-written if.
//
// The helpers here carry the setup, the comparison and the failure message so a
// test body reads as what it seeds and what it asserts.

// freshDirectory opens the test database, applies the schema, and clears every
// table, so each test starts from an empty directory.
func freshDirectory(t *testing.T) (*SQLDirectory, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	mustNoErr(t, "ensure schema", d.EnsureSchema())
	cleanTables(t, db)
	return d, db
}

// freshSQL opens the test database, applies the schema, clears every table, and
// returns the directory without exposing an unused *sql.DB handle.
func freshSQL(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	mustNoErr(t, "ensure schema", d.EnsureSchema())
	cleanTables(t, db)
	return d
}

// seedDomain provisions a domain under root and asserts an id was assigned.
func seedDomain(t *testing.T, d *SQLDirectory, root, name string) {
	t.Helper()
	if id := mustCreateDomain(t, d, root, name); id <= 0 {
		t.Fatalf("CreateDomain(%q) returned non-positive id %d", name, id)
	}
}

// seedUser provisions a user under root and asserts an id was assigned.
func seedUser(t *testing.T, d *SQLDirectory, root, addr, password string) {
	t.Helper()
	if id := mustCreateUser(t, d, root, addr, password); id <= 0 {
		t.Fatalf("CreateUser(%q) returned non-positive id %d", addr, id)
	}
}

// mustClose closes c and fails the test if Close returns an error.
func mustClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close %T: %v", c, err)
	}
}

// captureSink collects structured log events emitted by SQLDirectory in tests.
type captureSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *captureSink) Write(e logging.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *captureSink) find(name string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// names lists the captured event names under the lock, for a failure message.
func (s *captureSink) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, e.Name)
	}
	return out
}

// wantWarnEvent asserts sink captured a directory warn event with non-empty Err.
func wantWarnEvent(t *testing.T, sink *captureSink, name string) logging.Event {
	t.Helper()
	e, ok := sink.find(name)
	if !ok {
		t.Fatalf("expected logged event %q, got %+v", name, sink.names())
	}
	wantEq(t, name+" subsystem", e.Subsystem, logging.Directory)
	wantEq(t, name+" level", e.Level, logging.LevelWarn)
	if e.Err == "" {
		t.Errorf("event %q had empty Err field", name)
	}
	return e
}

// installFailTrigger installs a MariaDB trigger that rejects writes of event
// (INSERT, UPDATE, DELETE) on table, and removes it on test cleanup.
func installFailTrigger(t *testing.T, db *sql.DB, name, event, table string) {
	t.Helper()
	if _, err := db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS `%s`", name)); err != nil {
		t.Fatalf("drop stale trigger %s: %v", name, err)
	}
	q := fmt.Sprintf(
		"CREATE TRIGGER `%s` BEFORE %s ON `%s` FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected failure'",
		name, event, table,
	)
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS `%s`", name)); err != nil {
			t.Errorf("drop trigger %s: %v", name, err)
		}
	})
}
func mustCreateDomain(t *testing.T, d *SQLDirectory, root, name string) int64 {
	t.Helper()
	id, err := d.CreateDomain(name, filepath.Join(root, name))
	mustNoErr(t, "create domain "+name, err)
	return id
}

// mustCreateUser provisions a user with its own mailbox directory under root.
func mustCreateUser(t *testing.T, d *SQLDirectory, root, addr, password string) int64 {
	t.Helper()
	id, err := d.CreateUser(addr, password, filepath.Join(root, addr))
	mustNoErr(t, "create user "+addr, err)
	return id
}

// wantEq fails when got differs from want, naming the field in the message.
func wantEq[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// mustNoErr fails the test when err is set, naming the operation.
func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantErr fails when an operation the test requires to be refused succeeded.
func wantErr(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want one", what)
	}
}

// execRows returns a checker for a direct Exec: called as
// execRows(t, what, n)(db.Exec(...)), it fails on the error and requires the
// statement to have touched exactly n rows, so a fixture write that matched
// nothing cannot pass silently.
func execRows(t *testing.T, what string, want int64) func(sql.Result, error) {
	t.Helper()
	return func(res sql.Result, err error) {
		t.Helper()
		mustNoErr(t, what, err)
		n, err := res.RowsAffected()
		mustNoErr(t, "count the rows of "+what, err)
		if n != want {
			t.Fatalf("rows touched by %s = %d, want %d", what, n, want)
		}
	}
}

// execAtLeast is execRows for a statement whose exact count the test does not
// fix (clearing migration bookkeeping): it requires at least min rows.
func execAtLeast(t *testing.T, what string, min int64) func(sql.Result, error) {
	t.Helper()
	return func(res sql.Result, err error) {
		t.Helper()
		mustNoErr(t, what, err)
		n, err := res.RowsAffected()
		mustNoErr(t, "count the rows of "+what, err)
		if n < min {
			t.Fatalf("%s touched %d rows, want at least %d", what, n, min)
		}
	}
}

// hideTable renames a table away so every statement reading it fails, which is
// how a test forces a SELECT failure (a trigger can only fail a write). rename
// and restore are fixed RENAME TABLE statements; the returned func puts the table
// back, and t.Cleanup does so too if the test stops first.
func hideTable(t *testing.T, db *sql.DB, rename, restore string) func() {
	t.Helper()
	execRows(t, rename, 0)(db.Exec(rename))
	var restored bool
	put := func() {
		if restored {
			return
		}
		restored = true
		res, err := db.Exec(restore)
		if err != nil {
			t.Errorf("%s: %v", restore, err)
			return
		}
		if n, err := res.RowsAffected(); err != nil || n != 0 {
			t.Errorf("%s reported %d rows, %v", restore, n, err)
		}
	}
	t.Cleanup(put)
	return put
}

// countRows reads a COUNT(*) query's result.
func countRows(t *testing.T, db *sql.DB, label, query string, args ...any) int64 {
	t.Helper()
	var n int64
	mustNoErr(t, "count "+label, db.QueryRow(query, args...).Scan(&n))
	return n
}

// wantRows checks the row count a COUNT(*) query reports.
func wantRows(t *testing.T, db *sql.DB, label string, want int, query string, args ...any) {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", label, err)
	}
	wantEq(t, label, n, want)
}
