package directory

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestPurgeDomainCascade proves a domain purge removes the domain and everything
// scoped to it — users, aliases, forwards, fetchmail, altnames, mailing lists,
// and domain-scoped role permissions — while leaving another domain's data and a
// surviving role intact (the landmine: never delete rows that belong elsewhere).
func TestPurgeDomainCascade(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	acme, err := d.CreateDomain("acme.test", filepath.Join(root, "acme.test"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateDomain("other.test", filepath.Join(root, "other.test"))
	if err != nil {
		t.Fatal(err)
	}

	alice, err := d.CreateUser("alice@acme.test", "pw", filepath.Join(root, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.SetAltnames("alice@acme.test", []string{"alice2@acme.test"}); err != nil || !ok {
		t.Fatalf("SetAltnames = %v, %v", ok, err)
	}
	if err := d.CreateAlias("ali@acme.test", "alice@acme.test"); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.SetForward("alice@acme.test", 1, "elsewhere@x.test"); err != nil || !ok {
		t.Fatalf("SetForward = %v, %v", ok, err)
	}
	if _, err := d.CreateFetchmail(FetchmailEntry{Mailbox: "alice@acme.test", SrcServer: "imap.x", SrcUser: "a", Protocol: "IMAP"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateMList("list@acme.test", 0, 0); err != nil {
		t.Fatal(err)
	}

	// Landmine: another domain's user and its domain-scoped role permission must survive.
	if _, err := d.CreateUser("bob@other.test", "pw", filepath.Join(root, "bob")); err != nil {
		t.Fatal(err)
	}
	acmeStr, otherStr := strconv.FormatInt(acme, 10), strconv.FormatInt(other, 10)
	roleID, err := d.CreateRole("Helpdesk", "",
		[]Permission{
			{Name: PermDomainAdmin, Params: acmeStr},  // scoped to the purged domain — removed
			{Name: PermDomainAdmin, Params: otherStr}, // scoped elsewhere — survives
			{Name: PermSystemAdmin},                   // unscoped — survives
		},
		[]int64{alice})
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := d.PurgeDomain(acme, false); err != nil || !ok {
		t.Fatalf("PurgeDomain = %v, %v", ok, err)
	}

	// The domain and its rows are gone.
	if _, ok, _ := d.GetUser("alice@acme.test"); ok {
		t.Error("purged-domain user survived")
	}
	count := func(q string, args ...any) int {
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM domains WHERE id = ?`, acme); n != 0 {
		t.Errorf("domain row count = %d, want 0", n)
	}
	if n := count(`SELECT COUNT(*) FROM aliases WHERE mainname = ?`, "alice@acme.test"); n != 0 {
		t.Errorf("aliases survived: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM forwards WHERE username = ?`, "alice@acme.test"); n != 0 {
		t.Errorf("forwards survived: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM fetchmail WHERE mailbox = ?`, "alice@acme.test"); n != 0 {
		t.Errorf("fetchmail survived: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM altnames WHERE user_id = ?`, alice); n != 0 {
		t.Errorf("altnames survived (cascade): %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM mlists WHERE domain_id = ?`, acme); n != 0 {
		t.Errorf("mailing lists survived: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM role_permissions WHERE permission = ? AND params = ?`, PermDomainAdmin, acmeStr); n != 0 {
		t.Errorf("purged-domain role permission survived: %d", n)
	}

	// Landmine assertions: other domain, its user, its role permission, and the
	// role itself all survive.
	if n := count(`SELECT COUNT(*) FROM domains WHERE id = ?`, other); n != 1 {
		t.Errorf("other domain was touched: count %d, want 1", n)
	}
	if _, ok, _ := d.GetUser("bob@other.test"); !ok {
		t.Error("other domain's user was deleted by the purge")
	}
	if n := count(`SELECT COUNT(*) FROM role_permissions WHERE permission = ? AND params = ?`, PermDomainAdmin, otherStr); n != 1 {
		t.Errorf("other domain's role permission was deleted: count %d, want 1", n)
	}
	role, ok, err := d.GetRole(roleID)
	if err != nil || !ok {
		t.Fatalf("role was deleted by the purge: %v, %v", ok, err)
	}
	if !hasPerm(role.Permissions, PermSystemAdmin, "") {
		t.Errorf("role lost its unscoped permission: %+v", role.Permissions)
	}

	if ok, err := d.PurgeDomain(999999, false); ok || err != nil {
		t.Errorf("PurgeDomain(unknown) = %v, %v; want false, nil", ok, err)
	}
}

// TestPurgeDomainDeleteFiles proves deleteFiles removes the on-disk mailboxes and
// the domain directory, and leaves them when not requested.
func TestPurgeDomainDeleteFiles(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	mk := func(domain, user string) (id int64, homedir, maildir string) {
		homedir = filepath.Join(root, domain)
		maildir = filepath.Join(root, domain, user)
		if err := os.MkdirAll(maildir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(maildir, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := d.CreateDomain(domain, homedir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.CreateUser(user+"@"+domain, "pw", maildir); err != nil {
			t.Fatal(err)
		}
		return id, homedir, maildir
	}

	id, homedir, maildir := mk("purge.test", "u")
	if ok, err := d.PurgeDomain(id, true); err != nil || !ok {
		t.Fatalf("PurgeDomain(deleteFiles) = %v, %v", ok, err)
	}
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("maildir survived deleteFiles purge: %v", err)
	}
	if _, err := os.Stat(homedir); !os.IsNotExist(err) {
		t.Errorf("domain directory survived deleteFiles purge: %v", err)
	}

	// Without deleteFiles, the on-disk mailbox is left in place.
	id2, _, maildir2 := mk("keep.test", "u")
	if ok, err := d.PurgeDomain(id2, false); err != nil || !ok {
		t.Fatalf("PurgeDomain(noFiles) = %v, %v", ok, err)
	}
	if _, err := os.Stat(maildir2); err != nil {
		t.Errorf("maildir removed without deleteFiles: %v", err)
	}
}
