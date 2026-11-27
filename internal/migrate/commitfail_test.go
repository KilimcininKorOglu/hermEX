package migrate

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestAFailedCommitLeavesNoOpenTransaction runs a migration whose steps succeed
// but whose COMMIT fails: a deferred foreign key is still violated at commit, and
// SQLite then keeps the transaction open. The run must fail, and the pooled
// connection must not go back to the pool inside that transaction, or every later
// write on the database would run in (or wait on) a transaction nobody ends.
func TestAFailedCommitLeavesNoOpenTransaction(t *testing.T) {
	db := openSQLite(t)
	db.SetMaxOpenConns(1) // the next statement gets the connection the run used
	migs := []Migration{{Version: 1, Steps: []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE child (pid INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`INSERT INTO child (pid) VALUES (42)`,
	}}}
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs); err == nil {
		t.Fatal("a migration whose commit fails was reported as applied")
	}

	if _, err := db.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("a new transaction cannot start after the failed run: %v", err)
	}
	if _, err := db.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, db); v != 0 {
		t.Errorf("user_version = %d after the failed run, want 0", v)
	}
	if n := sqliteTableCount(t, db, "parent"); n != 0 {
		t.Errorf("parent table count = %d after failed commit, want 0", n)
	}
	if n := sqliteTableCount(t, db, "child"); n != 0 {
		t.Errorf("child table count = %d after failed commit, want 0", n)
	}
}

// TestAFailedStepLeavesNoHalfAppliedMigration fails the second statement of a
// migration. A schema left with the first statement applied but the version not
// recorded would make the next start re-run that statement and fail on it forever,
// so the run must fail and leave neither the statement nor the version behind.
func TestAFailedStepLeavesNoHalfAppliedMigration(t *testing.T) {
	db := openSQLite(t)
	migs := []Migration{{Version: 1, Steps: []string{
		`CREATE TABLE half (id INTEGER PRIMARY KEY)`,
		`INSERT INTO missing_table VALUES (1)`,
	}}}
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs); err == nil {
		t.Fatal("a migration with a failing statement was reported as applied")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'half'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the statement before the failing one stayed applied")
	}
	if v := userVersion(t, db); v != 0 {
		t.Errorf("user_version = %d after the failed run, want 0", v)
	}
}

// TestFailedVersionWriteRollsBackSchema proves a failure while recording the
// migration version rolls back the DDL executed in the same transaction and
// leaves no open transaction on the pooled connection.
func TestFailedVersionWriteRollsBackSchema(t *testing.T) {
	db := openSQLite(t)
	db.SetMaxOpenConns(1)
	writeErr := errors.New("version write refused")
	ver := SQLiteVersion{
		Read: UserVersion().Read,
		Write: func(context.Context, *sql.Conn, int) error {
			return writeErr
		},
	}
	migs := []Migration{{Version: 1, Steps: []string{`CREATE TABLE orphan (id INTEGER PRIMARY KEY)`}}}
	err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: ver}, 0, migs)
	if !errors.Is(err, writeErr) {
		t.Fatalf("run err = %v, want %v", err, writeErr)
	}
	if n := sqliteTableCount(t, db, "orphan"); n != 0 {
		t.Errorf("orphan table count = %d after version write failure, want 0", n)
	}
	if v := userVersion(t, db); v != 0 {
		t.Errorf("user_version = %d after version write failure, want 0", v)
	}
	if _, err := db.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("new transaction after version write failure: %v", err)
	}
	if _, err := db.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
}
