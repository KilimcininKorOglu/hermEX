package directory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSQLDirectoryUserDetailLifecycle exercises the admin detail/edit/delete
// path: GetUser reads the account record; UpdateUser writes the editable subset
// while preserving identity, the cached domain-status bits, and privilege bits
// it does not own; DeleteUser removes the user together with its aliases (which
// have no foreign key) so the address can be reused.
func TestSQLDirectoryUserDetailLifecycle(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	maildir := filepath.Join(root, "users", "alice")
	if _, err := d.CreateUser("alice@hermex.test", "secret", maildir); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("a.lias@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}

	// GetUser returns the freshly created account (case-insensitive lookup):
	// CreateUser grants pop3/imap and smtp, so both flags are set; it is not
	// LDAP-mastered and its status is normal (0).
	u, ok, err := d.GetUser("Alice@Hermex.Test")
	if err != nil || !ok {
		t.Fatalf("GetUser = %v, %v; want a user", ok, err)
	}
	if u.Username != "alice@hermex.test" || !u.POP3IMAP || !u.SMTP || u.LDAP || u.Status != 0 {
		t.Errorf("GetUser = %+v, want alice with pop3+smtp, no LDAP, status 0", u)
	}
	if _, ok, _ := d.GetUser("ghost@hermex.test"); ok {
		t.Error("GetUser(unknown) should report ok=false")
	}

	// UpdateUser writes the editable subset; identity (username/maildir) is
	// untouched.
	found, err := d.UpdateUser("alice@hermex.test", UserUpdate{
		Status: 1, Lang: "de", Timezone: "Europe/Berlin", DisplayType: 7, POP3IMAP: true, SMTP: false,
	})
	if err != nil || !found {
		t.Fatalf("UpdateUser = %v, %v; want found", found, err)
	}
	u, _, _ = d.GetUser("alice@hermex.test")
	if u.Status != 1 || u.Lang != "de" || u.Timezone != "Europe/Berlin" || u.DisplayType != 7 || !u.POP3IMAP || u.SMTP {
		t.Errorf("after update GetUser = %+v, want the edited subset", u)
	}
	if u.Maildir != maildir {
		t.Errorf("update changed the maildir to %q; identity must be immutable", u.Maildir)
	}
	if found, _ := d.UpdateUser("ghost@hermex.test", UserUpdate{}); found {
		t.Error("UpdateUser(unknown) should report found=false")
	}

	// The domain-status bits (0x30) are cached in address_status and must survive
	// a user-status edit — only the low nibble is replaced.
	if _, err := db.Exec(`UPDATE users SET address_status = 0x20 WHERE username = ?`, "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateUser("alice@hermex.test", UserUpdate{Status: 3, POP3IMAP: true}); err != nil {
		t.Fatal(err)
	}
	var rawStatus int
	if err := db.QueryRow(`SELECT address_status FROM users WHERE username = ?`, "alice@hermex.test").Scan(&rawStatus); err != nil {
		t.Fatal(err)
	}
	if rawStatus != 0x23 {
		t.Errorf("address_status = %#x after a status edit, want 0x23 (domain bits 0x20 preserved | status 3)", rawStatus)
	}

	// Privilege bits beyond the two hermEX defines (here 0x100) must survive an
	// edit that only toggles pop3/smtp.
	if _, err := db.Exec(`UPDATE users SET privilege_bits = 0x100 WHERE username = ?`, "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateUser("alice@hermex.test", UserUpdate{POP3IMAP: true, SMTP: false}); err != nil {
		t.Fatal(err)
	}
	var priv int
	if err := db.QueryRow(`SELECT privilege_bits FROM users WHERE username = ?`, "alice@hermex.test").Scan(&priv); err != nil {
		t.Fatal(err)
	}
	if priv != 0x101 {
		t.Errorf("privilege_bits = %#x, want 0x101 (foreign bit 0x100 preserved | pop3 set, smtp cleared)", priv)
	}

	// DeleteUser removes the alias too (no FK) so the address is free again; with
	// deleteFiles the maildir is removed from disk.
	if err := os.MkdirAll(maildir, 0o700); err != nil {
		t.Fatal(err)
	}
	gone, err := d.DeleteUser("alice@hermex.test", true)
	if err != nil || !gone {
		t.Fatalf("DeleteUser = %v, %v; want it existed", gone, err)
	}
	if _, ok, _ := d.GetUser("alice@hermex.test"); ok {
		t.Error("the user survived deletion")
	}
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("deleteFiles left the maildir at %q (stat err %v)", maildir, err)
	}
	var aliasRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM aliases WHERE aliasname = ?`, "a.lias@hermex.test").Scan(&aliasRows); err != nil {
		t.Fatal(err)
	}
	if aliasRows != 0 {
		t.Errorf("deleting the user left %d orphaned alias rows; the address would stay blocked", aliasRows)
	}
	if gone, _ := d.DeleteUser("alice@hermex.test", false); gone {
		t.Error("DeleteUser(already gone) should report it did not exist")
	}
}

// TestSQLDirectoryDeleteUserKeepsFiles proves a delete without deleteFiles leaves
// the maildir on disk — a missing flag must never destroy a mailbox's contents.
func TestSQLDirectoryDeleteUserKeepsFiles(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	maildir := filepath.Join(root, "users", "bob")
	if _, err := d.CreateUser("bob@hermex.test", "secret", maildir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(maildir, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := d.DeleteUser("bob@hermex.test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(maildir); err != nil {
		t.Errorf("a delete without deleteFiles removed the maildir at %q (stat err %v)", maildir, err)
	}
}

// TestSQLDirectoryAltnames proves alternative login names round-trip through
// Set/ListAltnames: the set is normalized and de-duplicated, a replace overwrites
// the prior set, an unknown user reports not-found, and a name already owned by
// another account is rejected with the prior set left intact.
func TestSQLDirectoryAltnames(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "pw", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("bob@hermex.test", "pw", filepath.Join(root, "bob")); err != nil {
		t.Fatal(err)
	}

	// Set normalizes (lowercase/trim), de-duplicates, and drops blanks.
	found, err := d.SetAltnames("alice@hermex.test", []string{"  Ali  ", "ali", "", "alice2"})
	if err != nil || !found {
		t.Fatalf("SetAltnames = %v, %v; want found", found, err)
	}
	got, err := d.ListAltnames("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "ali" || got[1] != "alice2" {
		t.Errorf("ListAltnames = %v, want [ali alice2] (normalized, deduped, ordered)", got)
	}

	// A replace overwrites the prior set entirely.
	if _, err := d.SetAltnames("alice@hermex.test", []string{"alice3"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.ListAltnames("alice@hermex.test"); len(got) != 1 || got[0] != "alice3" {
		t.Errorf("after replace ListAltnames = %v, want [alice3]", got)
	}

	// An unknown user is reported not-found.
	if found, _ := d.SetAltnames("ghost@hermex.test", []string{"x"}); found {
		t.Error("SetAltnames(unknown) should report not-found")
	}

	// A name owned by another account is rejected (the altname UNIQUE key), and
	// alice's set survives the rolled-back transaction.
	if _, err := d.SetAltnames("bob@hermex.test", []string{"bobalt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAltnames("alice@hermex.test", []string{"bobalt"}); err == nil {
		t.Error("SetAltnames with another user's altname should be rejected")
	}
	if got, _ := d.ListAltnames("alice@hermex.test"); len(got) != 1 || got[0] != "alice3" {
		t.Errorf("a rejected replace changed the set to %v, want [alice3] preserved", got)
	}
}

// TestSQLDirectoryUserAliases proves per-user e-mail aliases round-trip through
// Set/ListAliasesFor — normalized, de-duplicated, a replace overwrites, an
// unknown user is not-found, an in-use address is rejected with the prior set
// intact — and that a saved alias actually routes mail (Resolve follows it).
func TestSQLDirectoryUserAliases(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "dom")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "pw", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("bob@hermex.test", "pw", filepath.Join(root, "bob")); err != nil {
		t.Fatal(err)
	}

	found, err := d.SetAliasesFor("alice@hermex.test",
		[]string{"  Sales@Hermex.Test ", "sales@hermex.test", "", "info@hermex.test"})
	if err != nil || !found {
		t.Fatalf("SetAliasesFor = %v, %v; want found", found, err)
	}
	got, err := d.ListAliasesFor("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "info@hermex.test" || got[1] != "sales@hermex.test" {
		t.Errorf("ListAliasesFor = %v, want [info@ sales@] (normalized, deduped, ordered)", got)
	}
	// A saved alias must actually deliver to the user.
	if _, ok := d.Resolve("sales@hermex.test"); !ok {
		t.Error("a saved alias does not resolve to the user")
	}

	// A replace overwrites entirely; the dropped alias stops resolving.
	if _, err := d.SetAliasesFor("alice@hermex.test", []string{"only@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.ListAliasesFor("alice@hermex.test"); len(got) != 1 || got[0] != "only@hermex.test" {
		t.Errorf("after replace = %v, want [only@hermex.test]", got)
	}
	if _, ok := d.Resolve("sales@hermex.test"); ok {
		t.Error("a removed alias still resolves")
	}

	// Unknown user → not-found.
	if found, _ := d.SetAliasesFor("ghost@hermex.test", []string{"x@hermex.test"}); found {
		t.Error("SetAliasesFor(unknown) should report not-found")
	}

	// An address already in use is rejected and alice's set is preserved.
	if _, err := d.SetAliasesFor("bob@hermex.test", []string{"bobalias@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAliasesFor("alice@hermex.test", []string{"bobalias@hermex.test"}); err == nil {
		t.Error("SetAliasesFor with an in-use address should be rejected")
	}
	if got, _ := d.ListAliasesFor("alice@hermex.test"); len(got) != 1 || got[0] != "only@hermex.test" {
		t.Errorf("a rejected replace changed the set to %v, want [only@hermex.test] preserved", got)
	}
}
