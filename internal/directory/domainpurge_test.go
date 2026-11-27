package directory

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	seedUser(t, d, root, "bob@other.test", "password1")
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
	alice := mustCreateUser(t, d, root, "alice@acme.test", "password1")
	ok, err := d.SetAltnames("alice@acme.test", []string{"alice2@acme.test"})
	mustNoErr(t, "set altnames", err)
	wantEq(t, "SetAltnames found the user", ok, true)
	mustNoErr(t, "create alias", d.CreateAlias("ali@acme.test", "alice@acme.test"))
	ok, err = d.SetForward("alice@acme.test", 1, "elsewhere@x.test")
	mustNoErr(t, "set forward", err)
	wantEq(t, "SetForward found the user", ok, true)
	fid, err := d.CreateFetchmail(FetchmailEntry{Mailbox: "alice@acme.test", SrcServer: "imap.x", SrcUser: "a", Protocol: "IMAP"})
	mustNoErr(t, "create fetchmail entry", err)
	wantEq(t, "created fetchmail id > 0", fid > 0, true)
	lid, err := d.CreateMList("list@acme.test", 0, 0)
	mustNoErr(t, "create mailing list", err)
	wantEq(t, "created mlist id > 0", lid > 0, true)
	return alice
}

// wantDomainRowsGone proves the purge removed the domain and every row scoped to
// it.
func wantDomainRowsGone(t *testing.T, d *SQLDirectory, db *sql.DB, acme, alice int64, acmeStr string) {
	t.Helper()
	goneU, stillThere, err := d.GetUser("alice@acme.test")
	mustNoErr(t, "look up the purged user", err)
	wantEq(t, "purged-domain user present", stillThere, false)
	wantEq(t, "purged-domain user zero struct", goneU.Username, "")
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
	bobU, ok, err := d.GetUser("bob@other.test")
	mustNoErr(t, "look up the other domain's user", err)
	wantEq(t, "the other domain's user survived", ok, true)
	wantEq(t, "the other domain's username", bobU.Username, "bob@other.test")
	wantRows(t, db, "the other domain's role permission", 1,
		`SELECT COUNT(*) FROM role_permissions WHERE permission = ? AND params = ?`, PermDomainAdmin, otherStr)
	role := mustGetRole(t, d, roleID)
	wantPermissions(t, role.Permissions, []Permission{{Name: PermSystemAdmin}})
}

// TestPurgeDomainDeleteFiles proves deleteFiles removes the on-disk mailboxes and
// the domain directory, and leaves them when not requested.
func TestPurgeDomainDeleteFiles(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	purge := func(id int64, deleteFiles bool) {
		t.Helper()
		ok, err := d.PurgeDomain(id, deleteFiles)
		mustNoErr(t, "purge domain", err)
		wantEq(t, "PurgeDomain found the domain", ok, true)
	}

	id, homedir, maildir := seedDomainOnDisk(t, d, root, "purge.test")
	id2, homedir2, maildir2 := seedDomainOnDisk(t, d, root, "keep.test")
	wantEq(t, "distinct domain homedirs", homedir != homedir2, true)
	purge(id, true)
	if _, err := os.Stat(maildir); !os.IsNotExist(err) {
		t.Errorf("maildir survived a deleteFiles purge: %v", err)
	}
	if _, err := os.Stat(homedir); !os.IsNotExist(err) {
		t.Errorf("domain directory survived a deleteFiles purge: %v", err)
	}
	// Another domain's mailbox is not the purge's to delete.
	if _, err := os.Stat(filepath.Join(maildir2, "marker")); err != nil {
		t.Errorf("a deleteFiles purge removed another domain's mailbox: %v", err)
	}

	// Without deleteFiles, the on-disk mailbox is left in place.
	purge(id2, false)
	if _, err := os.Stat(maildir2); err != nil {
		t.Errorf("maildir removed without deleteFiles: %v", err)
	}
}

// TestPurgeDomainReportsFilesItCouldNotRemove proves a mailbox the purge cannot
// delete is reported, not left on disk without a trace. The rows are already
// gone by then, so the purge still counts as done (ok) and the error names the
// leftover; os.RemoveAll refuses a path ending in "." with EINVAL, which stands
// in for a storage failure the root-run dev container cannot produce by mode bits.
func TestPurgeDomainReportsFilesItCouldNotRemove(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	id, homedir, maildir := seedDomainOnDisk(t, d, root, "stuck.test")
	unremovable := maildir + "/."
	res, err := db.Exec(`UPDATE users SET maildir = ? WHERE username = ?`, unremovable, "u@stuck.test")
	mustNoErr(t, "point the mailbox at an unremovable path", err)
	wantAffected(t, "users pointed at an unremovable path", res)

	ok, err := d.PurgeDomain(id, true)
	wantEq(t, "PurgeDomain found the domain", ok, true)
	if !errors.Is(err, ErrPurgeFilesRemain) {
		t.Fatalf("PurgeDomain error = %v, want ErrPurgeFilesRemain", err)
	}
	wantRows(t, db, "domain rows after the purge", 0, `SELECT COUNT(*) FROM domains WHERE id = ?`, id)
	if !strings.Contains(err.Error(), unremovable) {
		t.Errorf("the error %q does not name the leftover %s", err, unremovable)
	}
	// One failed removal does not stop the rest: the domain directory is still removed.
	if _, err := os.Stat(homedir); !os.IsNotExist(err) {
		t.Errorf("the domain directory survived after one mailbox failed: %v", err)
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
	uid, err := d.CreateUser("u@"+domain, "password1", maildir)
	mustNoErr(t, "create user", err)
	wantEq(t, "created user id > 0", uid > 0, true)
	return id, homedir, maildir
}
