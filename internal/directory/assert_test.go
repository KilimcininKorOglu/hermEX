package directory

import (
	"database/sql"
	"path/filepath"
	"testing"
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

// mustCreateDomain provisions a domain under a fresh directory root.
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

// wantRows checks the row count a COUNT(*) query reports.
func wantRows(t *testing.T, db *sql.DB, label string, want int, query string, args ...any) {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", label, err)
	}
	wantEq(t, label, n, want)
}
