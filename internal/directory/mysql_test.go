package directory

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// openTestDB connects to the MariaDB given by HERMEX_TEST_MYSQL_DSN, skipping
// the test when it is unset (so the suite still runs without a database).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HERMEX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("HERMEX_TEST_MYSQL_DSN not set; skipping MariaDB directory test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// MariaDB may still be starting; ping with a bounded retry.
	var pingErr error
	for range 30 {
		if pingErr = db.Ping(); pingErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		db.Close()
		t.Fatalf("ping: %v", pingErr)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func cleanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, tbl := range []string{"altnames", "aliases", "users", "domains"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
}

func TestSQLDirectoryFaithfulResolution(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil {
		t.Fatal(err)
	}
	maildir := filepath.Join(root, "users", "hermex.test", "alice")
	if _, err := d.CreateUser("Alice@Hermex.Test", "secret", maildir); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("postmaster@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}

	// Resolution yields the maildir itself: that is the path handed to
	// objectstore.Open, which opens objects.sqlite3 + imapindex.sqlite3 inside it.
	wantMaildir := maildir

	// Authentication: correct password (case-insensitive login), wrong password,
	// and unknown user.
	if path, ok := d.Authenticate("alice@hermex.test", "secret"); !ok || path != wantMaildir {
		t.Errorf("Authenticate(correct) = %q, %v; want %q, true", path, ok, wantMaildir)
	}
	if _, ok := d.Authenticate("alice@hermex.test", "wrong"); ok {
		t.Error("Authenticate(wrong password) should fail")
	}
	if _, ok := d.Authenticate("ghost@hermex.test", "secret"); ok {
		t.Error("Authenticate(unknown user) should fail")
	}

	// Recipient resolution: the user, an alias to the user, and an unknown.
	if path, ok := d.Resolve("alice@hermex.test"); !ok || path != wantMaildir {
		t.Errorf("Resolve(user) = %q, %v; want %q, true", path, ok, wantMaildir)
	}
	if path, ok := d.Resolve("postmaster@hermex.test"); !ok || path != wantMaildir {
		t.Errorf("Resolve(alias) = %q, %v; want %q, true", path, ok, wantMaildir)
	}
	if _, ok := d.Resolve("nobody@hermex.test"); ok {
		t.Error("Resolve(unknown) should be refused")
	}

	// A suspended account (address_status != NORMAL) must not log in.
	if _, err := db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Authenticate("alice@hermex.test", "secret"); ok {
		t.Error("Authenticate should fail for a suspended account")
	}
}

// TestSQLDirectoryMaildirs checks that MailboxLister enumerates the store paths
// of active user mailboxes — the set the send-later spooler scans — and skips a
// suspended account, so the worker never releases mail on a disabled user's
// behalf.
func TestSQLDirectoryMaildirs(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil {
		t.Fatal(err)
	}
	aliceDir := filepath.Join(root, "users", "hermex.test", "alice")
	bobDir := filepath.Join(root, "users", "hermex.test", "bob")
	carolDir := filepath.Join(root, "users", "hermex.test", "carol")
	for addr, dir := range map[string]string{
		"alice@hermex.test": aliceDir,
		"bob@hermex.test":   bobDir,
		"carol@hermex.test": carolDir,
	} {
		if _, err := d.CreateUser(addr, "secret", dir); err != nil {
			t.Fatal(err)
		}
	}
	// Suspend carol: a disabled account's Outbox must not be scanned.
	if _, err := db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "carol@hermex.test"); err != nil {
		t.Fatal(err)
	}

	got, err := d.Maildirs()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{aliceDir: true, bobDir: true}
	if len(got) != len(want) {
		t.Fatalf("Maildirs = %v, want the 2 active maildirs (carol is suspended)", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected maildir %q (a suspended account leaked into the scan set)", p)
		}
	}
}
