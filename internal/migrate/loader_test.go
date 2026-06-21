package migrate

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// TestLoadFSParsesAndOrders proves the loader reads numbered files in version
// order, parses the leading number as the version, splits on semicolons, and
// drops comment and blank lines.
func TestLoadFSParsesAndOrders(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0002_more.sql":     {Data: []byte("-- add a column\nALTER TABLE a ADD COLUMN b INT;")},
		"m/0001_baseline.sql": {Data: []byte("CREATE TABLE a (id INTEGER PRIMARY KEY);\n\nCREATE INDEX ai ON a(id);\n")},
		"m/README.md":         {Data: []byte("not a migration")},
		"m/notes.txt":         {Data: []byte("ignored")},
	}
	migs, err := LoadFS(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 2 {
		t.Fatalf("loaded %d migrations, want 2", len(migs))
	}
	if migs[0].Version != 1 || migs[1].Version != 2 {
		t.Fatalf("versions = %d, %d; want 1, 2 (sorted)", migs[0].Version, migs[1].Version)
	}
	if len(migs[0].Steps) != 2 {
		t.Fatalf("v1 steps = %d, want 2 (CREATE TABLE + CREATE INDEX)", len(migs[0].Steps))
	}
	if len(migs[1].Steps) != 1 {
		t.Fatalf("v2 steps = %d, want 1 (comment line dropped)", len(migs[1].Steps))
	}
}

// TestLoadFSCommentSemicolon proves a semicolon inside a comment does not split a
// statement: comments are stripped before the file is split on semicolons.
func TestLoadFSCommentSemicolon(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_x.sql": {Data: []byte("-- holds bytes; and more text\nCREATE TABLE a (id INTEGER PRIMARY KEY);")},
	}
	migs, err := LoadFS(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 1 || len(migs[0].Steps) != 1 {
		t.Fatalf("got %d migrations with steps %v, want 1 migration with 1 step", len(migs), migs)
	}
	if !strings.HasPrefix(migs[0].Steps[0], "CREATE TABLE a") {
		t.Fatalf("statement = %q, want the CREATE only (comment with semicolon dropped)", migs[0].Steps[0])
	}
}

// TestLoadFSRejectsUnnumbered proves a file without a leading version number is an
// error rather than a silently skipped migration.
func TestLoadFSRejectsUnnumbered(t *testing.T) {
	fsys := fstest.MapFS{"m/baseline.sql": {Data: []byte("CREATE TABLE a (id INT);")}}
	if _, err := LoadFS(fsys, "m"); err == nil {
		t.Fatal("expected an error for an unnumbered migration file")
	}
}

// TestLoadFSRunsEndToEnd proves loaded statements apply through the runner: a real
// SQLite database is migrated to the loaded version with the schema in place.
func TestLoadFSRunsEndToEnd(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_init.sql": {Data: []byte("CREATE TABLE t (id INTEGER PRIMARY KEY, a TEXT);")},
		"m/0002_grow.sql": {Data: []byte("-- widen the row\nALTER TABLE t ADD COLUMN b TEXT;")},
	}
	migs, err := LoadFS(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	db := openSQLite(t)
	if err := Run(context.Background(), &SQLiteDriver{DB: db, Ver: UserVersion()}, 0, migs); err != nil {
		t.Fatalf("run loaded migrations: %v", err)
	}
	if v := userVersion(t, db); v != 2 {
		t.Fatalf("version after run = %d, want 2", v)
	}
	if _, err := db.Exec(`INSERT INTO t (a, b) VALUES ('x', 'y')`); err != nil {
		t.Fatalf("loaded schema missing a column: %v", err)
	}
}
