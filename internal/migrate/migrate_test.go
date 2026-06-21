package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

// openSQLite opens a fresh temp SQLite database with the same busy-timeout and
// WAL settings the real stores use, so the concurrency test exercises the same
// lock-wait behavior.
func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSQLiteRunApplyAndReRun proves a forward run applies every pending step once
// and a second run is a no-op. The first migration's step has no IF NOT EXISTS,
// so a re-application would fail with "table already exists" — the run-once
// guarantee is what keeps the second call green.
func TestSQLiteRunApplyAndReRun(t *testing.T) {
	db := openSQLite(t)
	migs := []Migration{
		{Version: 1, Steps: []string{`CREATE TABLE t (id INTEGER PRIMARY KEY, a TEXT)`}},
		{Version: 2, Steps: []string{`ALTER TABLE t ADD COLUMN b TEXT`}},
	}
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if v := userVersion(t, db); v != 2 {
		t.Fatalf("version after run = %d, want 2", v)
	}
	// Both columns exist (v2's ALTER ran).
	if _, err := db.Exec(`INSERT INTO t (a, b) VALUES ('x', 'y')`); err != nil {
		t.Fatalf("insert into migrated table: %v", err)
	}
	// Second run must do nothing — re-running v1's CREATE would error.
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs); err != nil {
		t.Fatalf("re-run should be a no-op, got: %v", err)
	}
}

// TestSQLiteBaselineNoOp proves a database already at the target version takes no
// action and no lock: the migration's destructive step would fail if it ran.
func TestSQLiteBaselineNoOp(t *testing.T) {
	db := openSQLite(t)
	if _, err := db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	migs := []Migration{{Version: 5, Steps: []string{`DROP TABLE does_not_exist`}}}
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 5, migs); err != nil {
		t.Fatalf("baseline no-op run: %v", err)
	}
}

// TestSQLiteDowngradeRefused proves a database newer than the binary is refused
// rather than touched.
func TestSQLiteDowngradeRefused(t *testing.T) {
	db := openSQLite(t)
	if _, err := db.Exec("PRAGMA user_version=9"); err != nil {
		t.Fatal(err)
	}
	migs := []Migration{{Version: 2, Steps: []string{`CREATE TABLE t (id INTEGER PRIMARY KEY)`}}}
	err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs)
	if !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade run err = %v, want ErrDowngrade", err)
	}
}

// TestSQLiteCustomVersionStore covers the object-store style version: a row in an
// application table rather than PRAGMA user_version, advanced from a non-zero
// baseline. It mirrors how the object store carries v25 forward.
func TestSQLiteCustomVersionStore(t *testing.T) {
	db := openSQLite(t)
	if _, err := db.Exec(`CREATE TABLE meta (k INTEGER PRIMARY KEY, v INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta (k, v) VALUES (1, 25)`); err != nil {
		t.Fatal(err)
	}
	ver := SQLiteVersion{
		Read: func(ctx context.Context, c *sql.Conn) (int, error) {
			var v int
			err := c.QueryRowContext(ctx, `SELECT v FROM meta WHERE k=1`).Scan(&v)
			return v, err
		},
		Write: func(ctx context.Context, c *sql.Conn, v int) error {
			_, err := c.ExecContext(ctx, `UPDATE meta SET v=? WHERE k=1`, v)
			return err
		},
	}
	migs := []Migration{{Version: 26, Steps: []string{`CREATE TABLE added (id INTEGER PRIMARY KEY)`}}}
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: ver}, 25, migs); err != nil {
		t.Fatalf("custom-store run: %v", err)
	}
	var v int
	if err := db.QueryRow(`SELECT v FROM meta WHERE k=1`).Scan(&v); err != nil || v != 26 {
		t.Fatalf("meta version = %d (err %v), want 26", v, err)
	}
	if _, err := db.Exec(`INSERT INTO added DEFAULT VALUES`); err != nil {
		t.Fatalf("v26 table missing: %v", err)
	}
}

// TestSQLiteConcurrentRunAppliesOnce proves two processes racing to migrate the
// same database serialize on the write lock and apply each step exactly once. The
// step has no IF NOT EXISTS, so a double application surfaces as a run error.
func TestSQLiteConcurrentRunAppliesOnce(t *testing.T) {
	db := openSQLite(t)
	migs := []Migration{{Version: 1, Steps: []string{`CREATE TABLE once (id INTEGER PRIMARY KEY)`}}}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: %v", i, err)
		}
	}
	if v := userVersion(t, db); v != 1 {
		t.Fatalf("version after race = %d, want 1", v)
	}
}

// TestValidateRejectsBadSets proves a malformed migration set is refused before
// any database work.
func TestValidateRejectsBadSets(t *testing.T) {
	bad := [][]Migration{
		{{Version: 0, Steps: nil}},
		{{Version: 2}, {Version: 1}},
		{{Version: 1}, {Version: 1}},
	}
	for i, migs := range bad {
		if err := Run(context.Background(), &SQLiteDriver{DB: openSQLite(t), Ver: UserVersion()}, 0, migs); err == nil {
			t.Errorf("set %d: expected validation error, got nil", i)
		}
	}
}

// openMySQL connects to an isolated migration test database on the shared dev
// MariaDB, separate from the directory suite's hermex_test so the shared
// schema_migrations table never collides across test binaries.
func openMySQL(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HERMEX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("HERMEX_TEST_MYSQL_DSN not set; skipping MariaDB migrate test")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	cfg.DBName = "hermex_migrate_test"
	admin := *cfg
	admin.DBName = ""
	adb, err := sql.Open("mysql", admin.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adb.Exec("CREATE DATABASE IF NOT EXISTS `" + cfg.DBName + "`"); err != nil {
		adb.Close()
		t.Fatalf("create test db: %v", err)
	}
	adb.Close()

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	// Start from a clean slate so each test is independent.
	for _, tbl := range []string{"schema_migrations", "mt1"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + tbl); err != nil {
			db.Close()
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestMySQLRunAndReRun proves the MariaDB driver applies pending migrations once,
// records them, and re-runs as a no-op. v1's CREATE has no IF NOT EXISTS, so a
// re-application would error.
func TestMySQLRunAndReRun(t *testing.T) {
	db := openMySQL(t)
	migs := []Migration{
		{Version: 1, Steps: []string{`CREATE TABLE mt1 (id INT UNSIGNED PRIMARY KEY) ENGINE=InnoDB`}},
		{Version: 2, Steps: []string{`ALTER TABLE mt1 ADD COLUMN note VARCHAR(32) NOT NULL DEFAULT ''`}},
	}
	if err := Run(context.Background(), &MySQLDriver{DB: db}, 0, migs); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var got int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&got); err != nil || got != 2 {
		t.Fatalf("recorded version = %d (err %v), want 2", got, err)
	}
	if _, err := db.Exec(`INSERT INTO mt1 (id, note) VALUES (1, 'ok')`); err != nil {
		t.Fatalf("v2 column missing: %v", err)
	}
	if err := Run(context.Background(), &MySQLDriver{DB: db}, 0, migs); err != nil {
		t.Fatalf("re-run should be a no-op, got: %v", err)
	}
}

// TestMySQLBaselineAdoption proves an existing, fully-populated database with no
// schema_migrations table is adopted as a clean no-op: v1's idempotent DDL leaves
// the existing table and its rows untouched, and the version is recorded.
func TestMySQLBaselineAdoption(t *testing.T) {
	db := openMySQL(t)
	if _, err := db.Exec(`CREATE TABLE mt1 (id INT UNSIGNED PRIMARY KEY) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mt1 (id) VALUES (1), (2), (3)`); err != nil {
		t.Fatal(err)
	}
	// v1 mirrors a real baseline: the current schema, idempotent.
	migs := []Migration{{Version: 1, Steps: []string{`CREATE TABLE IF NOT EXISTS mt1 (id INT UNSIGNED PRIMARY KEY) ENGINE=InnoDB`}}}
	if err := Run(context.Background(), &MySQLDriver{DB: db}, 0, migs); err != nil {
		t.Fatalf("adoption run: %v", err)
	}
	var rows, ver int
	if err := db.QueryRow("SELECT COUNT(*) FROM mt1").Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("existing rows = %d (err %v), want 3 — adoption must not recreate", rows, err)
	}
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&ver); err != nil || ver != 1 {
		t.Fatalf("recorded version = %d (err %v), want 1", ver, err)
	}
}

// TestMySQLDowngradeRefused proves a MariaDB database recorded newer than the
// binary is refused.
func TestMySQLDowngradeRefused(t *testing.T) {
	db := openMySQL(t)
	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (9, 0)"); err != nil {
		t.Fatal(err)
	}
	migs := []Migration{{Version: 2, Steps: []string{`CREATE TABLE mt1 (id INT UNSIGNED PRIMARY KEY) ENGINE=InnoDB`}}}
	err := Run(context.Background(), &MySQLDriver{DB: db}, 0, migs)
	if !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade run err = %v, want ErrDowngrade", err)
	}
}
