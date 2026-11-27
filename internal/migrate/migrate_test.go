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

func closeDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Errorf("close db: %v", err)
	}
}

func sqliteTableCount(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

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
	t.Cleanup(func() { closeDB(t, db) })
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
// so a re-application would fail with "table already exists", the run-once
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
	// Second run must do nothing, re-running v1's CREATE would error.
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
	if v := userVersion(t, db); v != 5 {
		t.Fatalf("version after baseline no-op = %d, want 5", v)
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
	if v := userVersion(t, db); v != 9 {
		t.Errorf("user_version = %d after refused downgrade, want 9", v)
	}
	if n := sqliteTableCount(t, db, "t"); n != 0 {
		t.Errorf("table t count = %d after refused downgrade, want 0", n)
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
//
// The file is created before the race: switching a new file to WAL is a separate
// race that each opener serializes itself (relay.Open), and it is not what this
// test measures.
func TestSQLiteConcurrentRunAppliesOnce(t *testing.T) {
	db := openSQLite(t)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
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
	step := []string{`CREATE TABLE should_not_exist (id INTEGER PRIMARY KEY)`}
	bad := [][]Migration{
		{{Version: -1, Steps: step}},
		{{Version: 0, Steps: step}},
		{{Version: 2, Steps: step}, {Version: 1, Steps: step}},
		{{Version: 1, Steps: step}, {Version: 1, Steps: step}},
	}
	for i, migs := range bad {
		db := openSQLite(t)
		if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs); err == nil {
			t.Errorf("set %d: expected validation error, got nil", i)
		}
		if v := userVersion(t, db); v != 0 {
			t.Errorf("set %d: user_version = %d after validation refusal, want 0", i, v)
		}
		if n := sqliteTableCount(t, db, "should_not_exist"); n != 0 {
			t.Errorf("set %d: table created despite validation refusal", i)
		}
	}
}

// TestSQLiteLockedDowngradeRefused proves the authoritative version re-read
// inside Lock refuses a database upgraded by a racing newer binary between the
// unlocked check and lock acquisition, leaving schema untouched and lock freed.
func TestSQLiteLockedDowngradeRefused(t *testing.T) {
	db := openSQLite(t)
	db.SetMaxOpenConns(1)
	reads := 0
	ver := SQLiteVersion{
		Read: func(context.Context, *sql.Conn) (int, error) {
			reads++
			if reads == 1 {
				return 0, nil
			}
			return 9, nil
		},
		Write: UserVersion().Write,
	}
	migs := []Migration{{Version: 2, Steps: []string{`CREATE TABLE t (id INTEGER PRIMARY KEY)`}}}
	err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: ver}, 0, migs)
	if !errors.Is(err, ErrDowngrade) {
		t.Fatalf("locked downgrade err = %v, want ErrDowngrade", err)
	}
	if n := sqliteTableCount(t, db, "t"); n != 0 {
		t.Errorf("table t count = %d after locked downgrade refusal, want 0", n)
	}
	if _, err := db.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("lock not released after locked downgrade refusal: %v", err)
	}
	if _, err := db.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteCanceledContextRefused proves Run refuses a canceled context and
// leaves the database schema and version unchanged.
func TestSQLiteCanceledContextRefused(t *testing.T) {
	db := openSQLite(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	migs := []Migration{{Version: 1, Steps: []string{`CREATE TABLE t (id INTEGER PRIMARY KEY)`}}}
	err := Run(ctx, &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context err = %v, want context.Canceled", err)
	}
	if v := userVersion(t, db); v != 0 {
		t.Errorf("user_version = %d after canceled run, want 0", v)
	}
	if n := sqliteTableCount(t, db, "t"); n != 0 {
		t.Errorf("table t count = %d after canceled run, want 0", n)
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
		closeDB(t, adb)
		t.Fatalf("create test db: %v", err)
	}
	closeDB(t, adb)

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	// Start from a clean slate so each test is independent.
	for _, tbl := range []string{"schema_migrations", "mt1"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + tbl); err != nil {
			closeDB(t, db)
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	t.Cleanup(func() { closeDB(t, db) })
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
		t.Fatalf("existing rows = %d (err %v), want 3, adoption must not recreate", rows, err)
	}
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&ver); err != nil || ver != 1 {
		t.Fatalf("recorded version = %d (err %v), want 1", ver, err)
	}
}

// TestMySQLDowngradeRefused proves a MariaDB database recorded newer than the
// binary is refused without changing the recorded version or creating tables.
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
	var ver, tables int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&ver); err != nil || ver != 9 {
		t.Errorf("recorded version = %d (err %v), want 9", ver, err)
	}
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'mt1'",
	).Scan(&tables); err != nil || tables != 0 {
		t.Errorf("table mt1 count = %d (err %v), want 0 after refused downgrade", tables, err)
	}
}

// TestMySQLFailedStepLeavesVersionUnrecorded proves a failing DDL/DML step in
// MySQLDriver returns an error and does not record the migration version.
func TestMySQLFailedStepLeavesVersionUnrecorded(t *testing.T) {
	db := openMySQL(t)
	migs := []Migration{{Version: 1, Steps: []string{`INSERT INTO missing_mysql_table VALUES (1)`}}}
	if err := Run(context.Background(), &MySQLDriver{DB: db}, 0, migs); err == nil {
		t.Fatal("expected error for failing MySQL migration step, got nil")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("schema_migrations row count = %d after failed step, want 0", count)
	}
}
