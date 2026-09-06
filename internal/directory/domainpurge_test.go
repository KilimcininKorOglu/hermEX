package directory

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestPurgeDomainCascade proves a domain purge removes the domain and everything
// scoped to it, users, aliases, forwards, fetchmail, altnames, mailing lists,
// and domain-scoped role permissions, while leaving another domain's data and a
// surviving role intact (the landmine: never delete rows that belong elsewhere).
func TestPurgeDomainCascade(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	acme := mustCreateDomain(t, d, root, "acme.test")
	other := mustCreateDomain(t, d, root, "other.test")
	alice := seedPurgeableUser(t, d, root)

	// Landmine: another domain's user and its domain-scoped role permission must survive.
	mustCreateUser(t, d, root, "bob@other.test", "pw")
	acmeStr, otherStr := strconv.FormatInt(acme, 10), strconv.FormatInt(other, 10)
	roleID, err := d.CreateRole("Helpdesk", "",
		[]Permission{
			{Name: PermDomainAdmin, Params: acmeStr},  // scoped to the purged domain, removed
			{Name: PermDomainAdmin, Params: otherStr}, // scoped elsewhere, survives
			{Name: PermSystemAdmin},                   // unscoped, survives
		},
		[]int64{alice})
	mustNoErr(t, "create role", err)

	purged, err := d.PurgeDomain(acme, false)
	mustNoErr(t, "purge domain", err)
	wantEq(t, "PurgeDomain reported the domain existed", purged, true)

	wantDomainRowsGone(t, d, db, acme, alice, acmeStr)
	wantOtherDomainIntact(t, d, db, other, otherStr, roleID)

	missing, err := d.PurgeDomain(999999, false)
	mustNoErr(t, "purge an unknown domain", err)
	wantEq(t, "PurgeDomain(unknown)", missing, false)
}

// seedPurgeableUser creates one user in acme.test carrying a row of every kind
// the purge must reach: an altname, an alias, a forward, a fetchmail entry, and
// a mailing list in the same domain.
func seedPurgeableUser(t *testing.T, d *SQLDirectory, root string) int64 {
	t.Helper()
	alice := mustCreateUser(t, d, root, "alice@acme.test", "pw")
	ok, err := d.SetAltnames("alice@acme.test", []string{"alice2@acme.test"})
	mustNoErr(t, "set altnames", err)
	wantEq(t, "SetAltnames found the user", ok, true)
	mustNoErr(t, "create alias", d.CreateAlias("ali@acme.test", "alice@acme.test"))
	ok, err = d.SetForward("alice@acme.test", 1, "elsewhere@x.test")
	mustNoErr(t, "set forward", err)
	wantEq(t, "SetForward found the user", ok, true)
	_, err = d.CreateFetchmail(FetchmailEntry{Mailbox: "alice@acme.test", SrcServer: "imap.x", SrcUser: "a", Protocol: "IMAP"})
	mustNoErr(t, "create fetchmail entry", err)
	_, err = d.CreateMList("list@acme.test", 0, 0)
	mustNoErr(t, "create mailing list", err)
	return alice
}

// wantDomainRowsGone proves the purge removed the domain and every row scoped to
// it.
func wantDomainRowsGone(t *testing.T, d *SQLDirectory, db *sql.DB, acme, alice int64, acmeStr string) {
	t.Helper()
	_, stillThere, _ := d.GetUser("alice@acme.test")
	wantEq(t, "purged-domain user present", stillThere, false)
	wantRows(t, db, "domain rows", 0, `SELECT COUNT(*) FROM domains WHERE id = ?`, acme)
	wantRows(t, db, "alias rows", 0, `SELECT COUNT(*) FROM aliases WHERE mainname = ?`, "alice@acme.test")
	wantRows(t, db, "forward rows", 0, `SELECT COUNT(*) FROM forwards WHERE username = ?`, "alice@acme.test")
	wantRows(t, db, "fetchmail rows", 0, `SELECT COUNT(*) FROM fetchmail WHERE mailbox = ?`, "alice@acme.test")
	wantRows(t, db, "altname rows (cascade)", 0, `SELECT COUNT(*) FROM altnames WHERE user_id = ?`, alice)
	wantRows(t, db, "mailing-list rows", 0, `SELECT COUNT(*) FROM mlists WHERE domain_id = ?`, acme)
	wantRows(t, db, "role permissions scoped to the purged domain", 0,
		`SELECT COUNT(*) FROM role_permissions WHERE permission = ? AND params = ?`, PermDomainAdmin, acmeStr)
}

// wantOtherDomainIntact is the landmine assertion: the other domain, its user,
// its role permission and the role itself all survive the purge.
func wantOtherDomainIntact(t *testing.T, d *SQLDirectory, db *sql.DB, other int64, otherStr string, roleID int64) {
	t.Helper()
	wantRows(t, db, "the other domain's rows", 1, `SELECT COUNT(*) FROM domains WHERE id = ?`, other)
	_, ok, _ := d.GetUser("bob@other.test")
	wantEq(t, "the other domain's user survived", ok, true)
	wantRows(t, db, "the other domain's role permission", 1,
		`SELECT COUNT(*) FROM role_permissions WHERE permission = ? AND params = ?`, PermDomainAdmin, otherStr)
	role := mustGetRole(t, d, roleID)
	wantPermissions(t, role.Permissions, []Permission{{Name: PermSystemAdmin}})
}

// TestPurgeDomainDeleteFiles proves deleteFiles removes the on-disk mailboxes and
// the domain directory, and leaves them when not requested.
func TestPurgeDomainDeleteFiles(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	purge := func(id int64, deleteFiles bool) {
		t.Helper()
		ok, err := d.PurgeDomain(id, deleteFiles)
		mustNoErr(t, "purge domain", err)
		wantEq(t, "PurgeDomain found the domain", ok, true)
	}

	id, homedir, maildir := seedDomainOnDisk(t, d, root, "purge.test")
	purge(id, true)
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("maildir survived a deleteFiles purge: %v", err)
	}
	if _, err := os.Stat(homedir); !os.IsNotExist(err) {
		t.Errorf("domain directory survived a deleteFiles purge: %v", err)
	}

	// Without deleteFiles, the on-disk mailbox is left in place.
	id2, _, maildir2 := seedDomainOnDisk(t, d, root, "keep.test")
	purge(id2, false)
	if _, err := os.Stat(maildir2); err != nil {
		t.Errorf("maildir removed without deleteFiles: %v", err)
	}
}

// seedDomainOnDisk creates a domain and one user whose directories exist on
// disk, so a purge can be observed removing them.
func seedDomainOnDisk(t *testing.T, d *SQLDirectory, root, domain string) (id int64, homedir, maildir string) {
	t.Helper()
	homedir = filepath.Join(root, domain)
	maildir = filepath.Join(root, domain, "u")
	mustNoErr(t, "create the maildir", os.MkdirAll(maildir, 0o755))
	mustNoErr(t, "write a marker file", os.WriteFile(filepath.Join(maildir, "marker"), []byte("x"), 0o644))
	id, err := d.CreateDomain(domain, homedir)
	mustNoErr(t, "create domain "+domain, err)
	_, err = d.CreateUser("u@"+domain, "pw", maildir)
	mustNoErr(t, "create user", err)
	return id, homedir, maildir
}
