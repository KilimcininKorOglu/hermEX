package objectstore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TableRecovery is what RecoverDatabase salvaged from one table.
type TableRecovery struct {
	Name       string
	Rows       int  // rows copied into the rebuilt database
	LostRows   int  // rows SQLite refused to read, each one skipped
	Unreadable bool // the table could not be read at all, so its whole content is gone
}

// RecoverReport is the outcome of rebuilding one damaged database.
type RecoverReport struct {
	Path   string          // the database that was rebuilt
	Backup string          // where the damaged file was moved
	Tables []TableRecovery // one entry per table, in the order they were copied
	Lost   int             // rows lost across every table
}

// RecoverMailboxDatabases rebuilds the named databases of the mailbox rooted at
// dir, holding the mailbox exclusively for the whole run. It reports
// ErrMailboxBusy without rebuilding anything when a daemon still has the mailbox
// open, because the rebuilt file replaces the one that daemon is reading.
//
// It returns the report of every database it attempted, including the one that
// failed, so a partial run is still accounted for.
func RecoverMailboxDatabases(dir string, names []string) ([]RecoverReport, error) {
	var out []RecoverReport
	err := withMailboxExclusive(dir, func() error {
		for _, name := range names {
			report, err := RecoverDatabase(filepath.Join(dir, name))
			out = append(out, report)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// rebuiltSuffix is appended to the database path while the rebuilt copy is being
// filled, so a run interrupted halfway leaves the damaged original in place.
const rebuiltSuffix = ".rebuilt"

// RecoverDatabase rebuilds a damaged SQLite database from the rows that are still
// readable. It copies the schema and then every table's rows into a new file,
// narrowing the copy around each range SQLite refuses to read, so one damaged page
// costs its own rows and not the table's. The damaged file is then moved aside
// (the returned Backup path) and the rebuilt one takes its place.
//
// THIS LOSES DATA. Every row on a damaged page is dropped, and the report counts
// them per table, because a rebuilt database that reads cleanly says nothing about
// what is no longer in it. A table SQLite cannot read at all is reported with
// Unreadable set and contributes no rows.
//
// The caller must hold the mailbox exclusively. The function does not take the
// lock itself, because a recovery is one step of a repair that holds the mailbox
// across all of its steps.
func RecoverDatabase(path string) (RecoverReport, error) {
	report := RecoverReport{Path: path}
	rebuilt := path + rebuiltSuffix
	if err := removeDatabaseFiles(rebuilt); err != nil {
		return report, err
	}
	db, err := openRebuildTarget(rebuilt, path)
	if err != nil {
		return report, err
	}
	report.Tables, err = rebuildInto(db)
	if err != nil {
		_ = db.Close()
		return report, err
	}
	if err := db.Close(); err != nil {
		return report, err
	}
	for _, t := range report.Tables {
		report.Lost += t.LostRows
	}
	report.Backup, err = swapInRebuilt(path, rebuilt)
	return report, err
}

// openRebuildTarget creates the rebuilt database and attaches the damaged one as
// "src". The pool is held to one connection, because ATTACH binds to a single
// connection and every later statement must see it.
func openRebuildTarget(rebuilt, damaged string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", rebuildDSN(rebuilt))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`ATTACH DATABASE ? AS src`, damaged); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("objectstore: attach %s: %w", damaged, err)
	}
	return db, nil
}

// rebuildDSN opens the rebuilt file without WAL, so the finished database is one
// file and the swap below moves no sidecar.
func rebuildDSN(path string) string {
	return "file:" + path + "?_pragma=journal_mode(DELETE)&_pragma=foreign_keys(0)&_pragma=busy_timeout(5000)"
}

// rebuildInto creates the damaged database's schema in the rebuilt one, copies
// every table, and then creates the indexes and triggers. The indexes come last so
// a salvaged row is not refused by an index the copy has not finished filling.
func rebuildInto(db *sql.DB) ([]TableRecovery, error) {
	tables, err := schemaStatements(db, true)
	if err != nil {
		return nil, err
	}
	rest, err := schemaStatements(db, false)
	if err != nil {
		return nil, err
	}
	for _, s := range tables {
		if _, err := db.Exec(s.ddl); err != nil {
			return nil, fmt.Errorf("objectstore: create %s: %w", s.name, err)
		}
	}
	out := make([]TableRecovery, 0, len(tables))
	for _, s := range tables {
		out = append(out, copyTable(db, s.name))
	}
	for _, s := range rest {
		// An index the salvaged rows violate is reported, not fatal: the data is
		// worth more than the index, and the operator is told which one is absent.
		if _, err := db.Exec(s.ddl); err != nil {
			out = append(out, TableRecovery{Name: s.name, Unreadable: true})
		}
	}
	return out, copyUserVersion(db)
}

// schemaObject is one entry of the damaged database's own schema.
type schemaObject struct{ name, ddl string }

// schemaStatements reads the damaged database's schema. wantTables selects the
// tables; its opposite selects the indexes, triggers and views. SQLite's internal
// objects carry no DDL and are skipped by the query.
func schemaStatements(db *sql.DB, wantTables bool) ([]schemaObject, error) {
	q := `SELECT name, sql FROM src.sqlite_master WHERE sql IS NOT NULL AND type != 'table'`
	if wantTables {
		q = `SELECT name, sql FROM src.sqlite_master WHERE sql IS NOT NULL AND type = 'table'`
	}
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemaObject
	for rows.Next() {
		var s schemaObject
		if err := rows.Scan(&s.name, &s.ddl); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// copyUserVersion carries the schema version the index database records in
// PRAGMA user_version, which no table holds and the rebuilt file would otherwise
// report as 0, making the store read as a downgrade.
func copyUserVersion(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA src.user_version`).Scan(&v); err != nil {
		return err
	}
	// PRAGMA takes no bound parameter; v came from SQLite as an integer.
	_, err := db.Exec(fmt.Sprintf("PRAGMA main.user_version=%d", v))
	return err
}

// copyTable copies one table's rows, narrowing around the ranges SQLite refuses.
func copyTable(db *sql.DB, name string) TableRecovery {
	tr := TableRecovery{Name: name}
	lo, hi, ok, err := rowidBounds(db, name)
	if err != nil {
		// No readable rowid range: try the table whole, and report it lost when
		// even that fails. A WITHOUT ROWID table lands here too.
		if n, e := insertRange(db, name, "", nil); e == nil {
			tr.Rows = n
		} else {
			tr.Unreadable = true
		}
		return tr
	}
	if !ok {
		return tr // the table is empty
	}
	salvageRange(func(lo, hi int64) (int, error) {
		return insertRange(db, name, ` WHERE rowid BETWEEN ? AND ?`, []any{lo, hi})
	}, lo, hi, &tr)
	return tr
}

// rangeCopier copies the rows of one rowid range and reports how many it copied.
// It is a parameter of salvageRange so the narrowing can be exercised against a
// chosen failure instead of against wherever a damaged page happens to land.
type rangeCopier func(lo, hi int64) (int, error)

// rowidBounds reads a table's lowest and highest rowid. ok is false for an empty
// table, which has neither and needs no copy.
func rowidBounds(db *sql.DB, name string) (lo, hi int64, ok bool, err error) {
	var a, b sql.NullInt64
	err = db.QueryRow(`SELECT MIN(rowid), MAX(rowid) FROM src.`+quoteIdent(name)).Scan(&a, &b)
	if err != nil {
		return 0, 0, false, err
	}
	if !a.Valid || !b.Valid {
		return 0, 0, false, nil
	}
	return a.Int64, b.Int64, true, nil
}

// salvageRange copies one rowid range, halving it whenever SQLite refuses to read
// it, so the rows outside the damaged page are still recovered. A single rowid
// that still fails is counted as one lost row.
func salvageRange(copy rangeCopier, lo, hi int64, tr *TableRecovery) {
	n, err := copy(lo, hi)
	if err == nil {
		tr.Rows += n
		return
	}
	if lo >= hi {
		tr.LostRows++
		return
	}
	mid := lo + (hi-lo)/2
	salvageRange(copy, lo, mid, tr)
	salvageRange(copy, mid+1, hi, tr)
}

// insertRange copies the rows one WHERE clause selects, inside a transaction so a
// read that fails part way leaves nothing behind and the caller can retry the
// halves without duplicating a row.
func insertRange(db *sql.DB, name, where string, args []any) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	q := quoteIdent(name)
	// #nosec G202 -- the table name comes from the damaged database's own sqlite_master and is quoted here
	res, err := tx.Exec(`INSERT INTO main.`+q+` SELECT * FROM src.`+q+where, args...)
	if err != nil {
		return 0, joinRollback(err, tx)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, joinRollback(err, tx)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// #nosec G115 -- a SQLite row count; it cannot exceed the rows the file holds
	return int(n), nil
}

// joinRollback rolls the failed copy back and returns the original failure.
func joinRollback(err error, tx *sql.Tx) error {
	_ = tx.Rollback()
	return err
}

// quoteIdent quotes a SQLite identifier, doubling any quote inside it.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// swapInRebuilt moves the damaged database and its journal sidecars aside and puts
// the rebuilt file in their place. It returns where the damaged file was moved, so
// the operator can keep it for a second attempt with other tools.
func swapInRebuilt(path, rebuilt string) (string, error) {
	backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	// The sidecars belong to the damaged file. Left in place they would be read as
	// the rebuilt database's own journal, which is a second corruption.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Rename(path+suffix, backup+suffix); err != nil && !os.IsNotExist(err) {
			return backup, err
		}
	}
	return backup, os.Rename(rebuilt, path)
}

// removeDatabaseFiles deletes a database and its journal sidecars, used to clear
// the leftovers of an interrupted rebuild.
func removeDatabaseFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
