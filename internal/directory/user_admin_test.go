package directory

import (
	"database/sql"
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
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	maildir := filepath.Join(root, "users", "alice")
	_, err := d.CreateUser("alice@hermex.test", "secret", maildir)
	mustNoErr(t, "create user", err)
	mustNoErr(t, "create alias", d.CreateAlias("a.lias@hermex.test", "alice@hermex.test"))

	// GetUser returns the freshly created account (case-insensitive lookup):
	// CreateUser grants pop3/imap and smtp, so both flags are set; it is not
	// LDAP-mastered and its status is normal (0).
	u := mustGetUser(t, d, "Alice@Hermex.Test")
	wantEq(t, "username", u.Username, "alice@hermex.test")
	wantEq(t, "POP3IMAP", u.POP3IMAP, true)
	wantEq(t, "SMTP", u.SMTP, true)
	wantEq(t, "LDAP-mastered", u.LDAP, false)
	wantEq(t, "status", u.Status, 0)
	_, ok, _ := d.GetUser("ghost@hermex.test")
	wantEq(t, "GetUser(unknown) found", ok, false)

	// UpdateUser writes the editable subset; identity (username/maildir) is
	// untouched.
	found, err := d.UpdateUser("alice@hermex.test", UserUpdate{
		Status: 1, Lang: "de", Timezone: "Europe/Berlin", DisplayType: 7, POP3IMAP: true, SMTP: false,
	})
	mustNoErr(t, "update user", err)
	wantEq(t, "UpdateUser found the user", found, true)
	u = mustGetUser(t, d, "alice@hermex.test")
	wantEq(t, "status after update", u.Status, 1)
	wantEq(t, "lang after update", u.Lang, "de")
	wantEq(t, "timezone after update", u.Timezone, "Europe/Berlin")
	wantEq(t, "display type after update", u.DisplayType, 7)
	wantEq(t, "POP3IMAP after update", u.POP3IMAP, true)
	wantEq(t, "SMTP after update", u.SMTP, false)
	wantEq(t, "maildir after update (identity is immutable)", u.Maildir, maildir)
	unknown, _ := d.UpdateUser("ghost@hermex.test", UserUpdate{})
	wantEq(t, "UpdateUser(unknown) found", unknown, false)

	wantForeignBitsPreserved(t, d, db)
	wantUserDeleteFreesAddress(t, d, db, maildir)
}

// wantForeignBitsPreserved proves an edit replaces only the bits it owns: the
// domain-status bits cached in address_status (0x30) survive a user-status edit,
// and a privilege bit hermEX does not define survives a pop3/smtp toggle.
func wantForeignBitsPreserved(t *testing.T, d *SQLDirectory, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`UPDATE users SET address_status = 0x20 WHERE username = ?`, "alice@hermex.test")
	mustNoErr(t, "seed the domain-status bits", err)
	_, err = d.UpdateUser("alice@hermex.test", UserUpdate{Status: 3, POP3IMAP: true})
	mustNoErr(t, "update user", err)
	var rawStatus int
	mustNoErr(t, "read address_status",
		db.QueryRow(`SELECT address_status FROM users WHERE username = ?`, "alice@hermex.test").Scan(&rawStatus))
	wantEq(t, "address_status after a status edit (domain bits 0x20 preserved, status 3)", rawStatus, 0x23)

	_, err = db.Exec(`UPDATE users SET privilege_bits = 0x100 WHERE username = ?`, "alice@hermex.test")
	mustNoErr(t, "seed a foreign privilege bit", err)
	_, err = d.UpdateUser("alice@hermex.test", UserUpdate{POP3IMAP: true, SMTP: false})
	mustNoErr(t, "update user", err)
	var priv int
	mustNoErr(t, "read privilege_bits",
		db.QueryRow(`SELECT privilege_bits FROM users WHERE username = ?`, "alice@hermex.test").Scan(&priv))
	wantEq(t, "privilege_bits (foreign bit 0x100 preserved, pop3 set, smtp cleared)", priv, 0x101)
}

// wantUserDeleteFreesAddress proves a delete removes the alias too (it has no
// foreign key) so the address is reusable, and that deleteFiles takes the
// maildir with it.
func wantUserDeleteFreesAddress(t *testing.T, d *SQLDirectory, db *sql.DB, maildir string) {
	t.Helper()
	mustNoErr(t, "create the maildir", os.MkdirAll(maildir, 0o700))
	gone, err := d.DeleteUser("alice@hermex.test", true)
	mustNoErr(t, "delete user", err)
	wantEq(t, "DeleteUser reported the user existed", gone, true)
	_, stillThere, _ := d.GetUser("alice@hermex.test")
	wantEq(t, "user present after deletion", stillThere, false)
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("deleteFiles left the maildir at %q (stat err %v)", maildir, err)
	}
	wantRows(t, db, "orphaned alias rows (they would keep the address blocked)", 0,
		`SELECT COUNT(*) FROM aliases WHERE aliasname = ?`, "a.lias@hermex.test")
	again, _ := d.DeleteUser("alice@hermex.test", false)
	wantEq(t, "DeleteUser(already gone)", again, false)
}

// mustGetUser reads a user back, requiring the account to exist.
func mustGetUser(t *testing.T, d *SQLDirectory, login string) UserDetail {
	t.Helper()
	u, ok, err := d.GetUser(login)
	mustNoErr(t, "get user", err)
	if !ok {
		t.Fatalf("user %q not found", login)
	}
	return u
}

// TestSQLDirectoryDeleteUserKeepsFiles proves a delete without deleteFiles leaves
// the maildir on disk, a missing flag must never destroy a mailbox's contents.
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
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "pw")
	mustCreateUser(t, d, root, "bob@hermex.test", "pw")

	// Set normalizes (lowercase/trim), de-duplicates, and drops blanks.
	found, err := d.SetAltnames("alice@hermex.test", []string{"  Ali  ", "ali", "", "alice2"})
	mustNoErr(t, "set altnames", err)
	wantEq(t, "SetAltnames found the user", found, true)
	wantAltnames(t, d, "normalized, deduped and ordered", "ali", "alice2")

	// A replace overwrites the prior set entirely.
	_, err = d.SetAltnames("alice@hermex.test", []string{"alice3"})
	mustNoErr(t, "replace altnames", err)
	wantAltnames(t, d, "after the replace", "alice3")

	// An unknown user is reported not-found.
	ghost, _ := d.SetAltnames("ghost@hermex.test", []string{"x"})
	wantEq(t, "SetAltnames(unknown) found a user", ghost, false)

	// A name owned by another account is rejected (the altname UNIQUE key), and
	// alice's set survives the rolled-back transaction.
	_, err = d.SetAltnames("bob@hermex.test", []string{"bobalt"})
	mustNoErr(t, "set bob's altnames", err)
	_, err = d.SetAltnames("alice@hermex.test", []string{"bobalt"})
	wantErr(t, "SetAltnames accepted another user's altname", err)
	wantAltnames(t, d, "after the rejected replace", "alice3")
}

// wantAltnames checks alice's altname set is exactly the given names, in order.
func wantAltnames(t *testing.T, d *SQLDirectory, what string, want ...string) {
	t.Helper()
	got, err := d.ListAltnames("alice@hermex.test")
	mustNoErr(t, "list altnames", err)
	if len(got) != len(want) {
		t.Fatalf("altnames %s = %v, want %v", what, got, want)
	}
	for i := range want {
		wantEq(t, "altname "+what, got[i], want[i])
	}
}

// TestSQLDirectoryUserAliases proves per-user e-mail aliases round-trip through
// Set/ListAliasesFor, normalized, de-duplicated, a replace overwrites, an
// unknown user is not-found, an in-use address is rejected with the prior set
// intact, and that a saved alias actually routes mail (Resolve follows it).
func TestSQLDirectoryUserAliases(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "pw")
	mustCreateUser(t, d, root, "bob@hermex.test", "pw")

	found, err := d.SetAliasesFor("alice@hermex.test",
		[]string{"  Sales@Hermex.Test ", "sales@hermex.test", "", "info@hermex.test"})
	mustNoErr(t, "set aliases", err)
	wantEq(t, "SetAliasesFor found the user", found, true)
	wantAliases(t, d, "normalized, deduped and ordered", "info@hermex.test", "sales@hermex.test")
	// A saved alias must actually deliver to the user.
	_, resolves := d.Resolve("sales@hermex.test")
	wantEq(t, "a saved alias resolves to the user", resolves, true)

	// A replace overwrites entirely; the dropped alias stops resolving.
	_, err = d.SetAliasesFor("alice@hermex.test", []string{"only@hermex.test"})
	mustNoErr(t, "replace aliases", err)
	wantAliases(t, d, "after the replace", "only@hermex.test")
	_, stillResolves := d.Resolve("sales@hermex.test")
	wantEq(t, "a removed alias still resolves", stillResolves, false)

	// Unknown user → not-found.
	ghost, _ := d.SetAliasesFor("ghost@hermex.test", []string{"x@hermex.test"})
	wantEq(t, "SetAliasesFor(unknown) found a user", ghost, false)

	// An address already in use is rejected and alice's set is preserved.
	_, err = d.SetAliasesFor("bob@hermex.test", []string{"bobalias@hermex.test"})
	mustNoErr(t, "set bob's aliases", err)
	_, err = d.SetAliasesFor("alice@hermex.test", []string{"bobalias@hermex.test"})
	wantErr(t, "SetAliasesFor accepted an in-use address", err)
	wantAliases(t, d, "after the rejected replace", "only@hermex.test")
}

// wantAliases checks alice's alias set is exactly the given addresses, in order.
func wantAliases(t *testing.T, d *SQLDirectory, what string, want ...string) {
	t.Helper()
	got, err := d.ListAliasesFor("alice@hermex.test")
	mustNoErr(t, "list aliases", err)
	if len(got) != len(want) {
		t.Fatalf("aliases %s = %v, want %v", what, got, want)
	}
	for i := range want {
		wantEq(t, "alias "+what, got[i], want[i])
	}
}

// TestSQLDirectoryUserProperties proves the EAV property store round-trips and,
// critically, that SetUserProperties touches ONLY the proptags it is given: a
// property written by another subsystem survives an unrelated contact edit, and
// an empty value clears just its own proptag.
func TestSQLDirectoryUserProperties(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "pw")
	var uid int64
	mustNoErr(t, "read alice's user id",
		db.QueryRow(`SELECT id FROM users WHERE username = ?`, "alice@hermex.test").Scan(&uid))
	// A property owned by another subsystem, a tag the contact editor never manages.
	const foreignTag = 0x0FFF001F
	_, err := db.Exec(`INSERT INTO user_properties (user_id, proptag, order_id, propval_str) VALUES (?, ?, 1, ?)`,
		uid, foreignTag, "do-not-touch")
	mustNoErr(t, "seed a foreign property", err)

	const prDisplayName, prNickname = 0x3001001F, 0x3A4F001F
	found, err := d.SetUserProperties("alice@hermex.test", map[uint32]string{
		prDisplayName: "Alice Liddell",
		prNickname:    "Ali",
	})
	mustNoErr(t, "set user properties", err)
	wantEq(t, "SetUserProperties found the user", found, true)
	got := mustUserProperties(t, d)
	wantEq(t, "display name", got[prDisplayName], "Alice Liddell")
	wantEq(t, "nickname", got[prNickname], "Ali")
	// The blocking correctness point: the foreign property survives a contact edit.
	wantEq(t, "the foreign property after a contact edit (the table must not be wholesale-replaced)",
		got[foreignTag], "do-not-touch")

	// An empty value clears only that one proptag; the others (and the foreign
	// one) are untouched.
	_, err = d.SetUserProperties("alice@hermex.test", map[uint32]string{prNickname: ""})
	mustNoErr(t, "clear the nickname", err)
	got = mustUserProperties(t, d)
	_, stillSet := got[prNickname]
	wantEq(t, "nickname present after an empty write", stillSet, false)
	wantEq(t, "display name after clearing the nickname", got[prDisplayName], "Alice Liddell")
	wantEq(t, "the foreign property after clearing the nickname", got[foreignTag], "do-not-touch")

	// Unknown user → not found.
	ghost, _ := d.SetUserProperties("ghost@hermex.test", map[uint32]string{prDisplayName: "x"})
	wantEq(t, "SetUserProperties(unknown) found a user", ghost, false)
}

// mustUserProperties reads alice's property bag.
func mustUserProperties(t *testing.T, d *SQLDirectory) map[uint32]string {
	t.Helper()
	got, err := d.GetUserProperties("alice@hermex.test")
	mustNoErr(t, "get user properties", err)
	return got
}
