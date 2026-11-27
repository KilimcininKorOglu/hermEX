package directory

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// openTestDB connects to the MariaDB given by HERMEX_TEST_MYSQL_DSN, skipping
// the test when it is unset (so the suite still runs without a database).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HERMEX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("HERMEX_TEST_MYSQL_DSN not set; skipping MariaDB directory test")
	}
	// The DSN names a dedicated test database on the shared dev MariaDB, kept
	// separate from the runtime 'email' database so the suite never touches live
	// accounts. Create it on demand: connect with the schema name cleared, issue
	// CREATE DATABASE IF NOT EXISTS, then open the real DSN.
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse HERMEX_TEST_MYSQL_DSN: %v", err)
	}
	dbName := cfg.DBName
	ensureTestDatabase(t, cfg, dbName)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		closeLog(t, "test db", db)
		t.Fatalf("ping %q: %v", dbName, err)
	}
	t.Cleanup(func() { mustClose(t, db) })
	return db
}

func closeLog(t *testing.T, what string, c interface{ Close() error }) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Logf("close %s: %v", what, err)
	}
}

func ensureTestDatabase(t *testing.T, cfg *mysqldriver.Config, dbName string) {
	t.Helper()
	adminCfg := *cfg
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	var pingErr error
	for range 30 {
		if pingErr = admin.Ping(); pingErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		closeLog(t, "admin db", admin)
		t.Fatalf("ping: %v", pingErr)
	}
	res, err := admin.Exec("CREATE DATABASE IF NOT EXISTS `" + dbName + "`")
	if err != nil {
		closeLog(t, "admin db", admin)
		t.Fatalf("create test database %q: %v", dbName, err)
	}
	// MariaDB reports 1 for a database it created and 0 for one that existed.
	created, err := res.RowsAffected()
	mustNoErr(t, "read the create-database result", err)
	if created > 1 {
		t.Fatalf("create test database %q affected %d rows, want 0 or 1", dbName, created)
	}
	mustClose(t, admin)
}

func cleanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	before := countRows(t, db, "tables to clean", cleanedRowsQuery)
	var removed int64
	for _, stmt := range cleanStatements {
		res, err := db.Exec(stmt)
		if err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		n, err := res.RowsAffected()
		mustNoErr(t, "count the rows of "+stmt, err)
		removed += n
	}
	// Every listed table is in cleanedRowsQuery too, so the rows removed equal the
	// rows there were, and none are left; a table missing from either list shows.
	wantEq(t, "rows removed by cleanTables", removed, before)
	wantEq(t, "rows left after cleanTables", countRows(t, db, "cleaned tables", cleanedRowsQuery), int64(0))
}

// cleanStatements empties each table, children before parents (FK order).
var cleanStatements = []string{
	`DELETE FROM app_passwords`, `DELETE FROM user_totp_recovery`, `DELETE FROM user_totp`,
	`DELETE FROM altnames`, `DELETE FROM aliases`, `DELETE FROM forwards`,
	`DELETE FROM fetchmail_seen`, `DELETE FROM fetchmail`, `DELETE FROM admin_roles`,
	`DELETE FROM user_roles`, `DELETE FROM role_permissions`, `DELETE FROM roles`,
	`DELETE FROM associations`, `DELETE FROM specifieds`, `DELETE FROM mlists`,
	`DELETE FROM ldap_bindings`, `DELETE FROM ldap_connections`, `DELETE FROM users`,
	`DELETE FROM domains`, `DELETE FROM orgs`, `DELETE FROM ldap_config`,
	`DELETE FROM sync_policy`, `DELETE FROM create_defaults`, `DELETE FROM active_sessions`,
	`DELETE FROM webmail_sessions`, `DELETE FROM admin_sessions`, `DELETE FROM push_subscriptions`,
	`DELETE FROM dkim_keys`, `DELETE FROM tls_certs`, `DELETE FROM smtp_gateways`,
	`DELETE FROM conn_limit_settings`, `DELETE FROM dmarc_report_records`, `DELETE FROM dmarc_reports`,
	`DELETE FROM tlsrpt_report_policies`, `DELETE FROM tlsrpt_reports`, `DELETE FROM dmarc_failure_reports`,
	`DELETE FROM mail_report_settings`, `DELETE FROM dmarc_report_settings`, `DELETE FROM av_quarantine`,
	`DELETE FROM health_targets`, `DELETE FROM password_policy_settings`,
}

// cleanedRowsQuery sums the rows of every table cleanTables empties.
const cleanedRowsQuery = `SELECT
 (SELECT COUNT(*) FROM app_passwords) + (SELECT COUNT(*) FROM user_totp_recovery) + (SELECT COUNT(*) FROM user_totp) +
 (SELECT COUNT(*) FROM altnames) + (SELECT COUNT(*) FROM aliases) + (SELECT COUNT(*) FROM forwards) +
 (SELECT COUNT(*) FROM fetchmail_seen) + (SELECT COUNT(*) FROM fetchmail) + (SELECT COUNT(*) FROM admin_roles) +
 (SELECT COUNT(*) FROM user_roles) + (SELECT COUNT(*) FROM role_permissions) + (SELECT COUNT(*) FROM roles) +
 (SELECT COUNT(*) FROM associations) + (SELECT COUNT(*) FROM specifieds) + (SELECT COUNT(*) FROM mlists) +
 (SELECT COUNT(*) FROM ldap_bindings) + (SELECT COUNT(*) FROM ldap_connections) + (SELECT COUNT(*) FROM users) +
 (SELECT COUNT(*) FROM domains) + (SELECT COUNT(*) FROM orgs) + (SELECT COUNT(*) FROM ldap_config) +
 (SELECT COUNT(*) FROM sync_policy) + (SELECT COUNT(*) FROM create_defaults) + (SELECT COUNT(*) FROM active_sessions) +
 (SELECT COUNT(*) FROM webmail_sessions) + (SELECT COUNT(*) FROM admin_sessions) + (SELECT COUNT(*) FROM push_subscriptions) +
 (SELECT COUNT(*) FROM dkim_keys) + (SELECT COUNT(*) FROM tls_certs) + (SELECT COUNT(*) FROM smtp_gateways) +
 (SELECT COUNT(*) FROM conn_limit_settings) + (SELECT COUNT(*) FROM dmarc_report_records) + (SELECT COUNT(*) FROM dmarc_reports) +
 (SELECT COUNT(*) FROM tlsrpt_report_policies) + (SELECT COUNT(*) FROM tlsrpt_reports) + (SELECT COUNT(*) FROM dmarc_failure_reports) +
 (SELECT COUNT(*) FROM mail_report_settings) + (SELECT COUNT(*) FROM dmarc_report_settings) + (SELECT COUNT(*) FROM av_quarantine) +
 (SELECT COUNT(*) FROM health_targets) + (SELECT COUNT(*) FROM password_policy_settings)`

// TestForwardDirective covers the forwards model: a directive set on a user is read
// back, an alias to that user resolves to the same directive (canonical keying, mail
// to an alias must not bypass the forward), clearing removes it, an unknown user is
// reported absent, and DeleteUser leaves no orphan forward row.
func TestForwardDirective(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "alice@hermex.test", "secret-pw1")
	mustNoErr(t, "create alias", d.CreateAlias("sales@hermex.test", "alice@hermex.test"))

	// Set a Redirect and read it back by canonical username.
	existed, err := d.SetForward("alice@hermex.test", ForwardRedirect, "boss@external.test")
	mustNoErr(t, "set forward", err)
	wantEq(t, "SetForward found the user", existed, true)
	fi := mustGetForward(t, d, "alice@hermex.test")
	wantEq(t, "forward type", fi.Type, ForwardRedirect)
	wantEq(t, "forward destination", fi.Destination, "boss@external.test")
	// An alias to the user must resolve to the same directive, keying on the raw
	// alias would let mail to sales@ bypass alice's forward.
	wantEq(t, "the alias resolves to the user's directive",
		mustGetForward(t, d, "sales@hermex.test").Destination, "boss@external.test")

	// A destination that is not one routable address is refused and the stored
	// directive is kept: under a Redirect it would bounce every inbound message.
	for _, bad := range []string{"boss", "a@x.test, b@x.test", "Boss <boss@x.test>", "boss@x_y.test"} {
		ok, err := d.SetForward("alice@hermex.test", ForwardRedirect, bad)
		wantErr(t, "SetForward refused "+bad, err)
		wantEq(t, "refused SetForward found for "+bad, ok, false)
	}
	wantEq(t, "the destination after refused writes",
		mustGetForward(t, d, "alice@hermex.test").Destination, "boss@external.test")

	// An empty destination clears the forward.
	existed, err = d.SetForward("alice@hermex.test", ForwardCC, "")
	mustNoErr(t, "clear forward", err)
	wantEq(t, "SetForward(clear) found the user", existed, true)
	emptyF, ok, err := d.GetForward("alice@hermex.test")
	mustNoErr(t, "get forward", err)
	wantEq(t, "forward present after clearing", ok, false)
	wantEq(t, "cleared forward zero value", emptyF, ForwardInfo{})

	// An unknown user is reported absent, not created.
	existed, err = d.SetForward("ghost@hermex.test", ForwardCC, "x@y.test")
	mustNoErr(t, "set a forward on an unknown user", err)
	wantEq(t, "SetForward(unknown) found a user", existed, false)

	// DeleteUser leaves no orphan forward row.
	existed, err = d.SetForward("alice@hermex.test", ForwardCC, "boss@external.test")
	mustNoErr(t, "set forward", err)
	wantEq(t, "SetForward found alice", existed, true)
	deleted, err := d.DeleteUser("alice@hermex.test", false)
	mustNoErr(t, "delete user", err)
	wantEq(t, "DeleteUser found alice", deleted, true)
	wantRows(t, db, "forward rows after DeleteUser", 0,
		`SELECT COUNT(*) FROM forwards WHERE username = ?`, "alice@hermex.test")
}

// mustGetForward reads a forward directive, requiring one to be set.
func mustGetForward(t *testing.T, d *SQLDirectory, address string) ForwardInfo {
	t.Helper()
	fi, ok, err := d.GetForward(address)
	mustNoErr(t, "get forward", err)
	if !ok {
		t.Fatalf("no forward set for %q", address)
	}
	return fi
}

func TestSQLDirectoryFaithfulResolution(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	maildir := filepath.Join(root, "users", "hermex.test", "alice")
	uid, err := d.CreateUser("Alice@Hermex.Test", "secret-pw1", maildir)
	mustNoErr(t, "create user", err)
	wantEq(t, "created alice id > 0", uid > 0, true)
	mustNoErr(t, "create alias", d.CreateAlias("postmaster@hermex.test", "alice@hermex.test"))

	// Resolution yields the maildir itself: that is the path handed to
	// objectstore.Open, which opens objects.sqlite3 + imapindex.sqlite3 inside it.

	// Authentication: correct password (case-insensitive login), wrong password,
	// and unknown user.
	path, ok := d.Authenticate("alice@hermex.test", "secret-pw1")
	wantEq(t, "Authenticate admitted the correct password", ok, true)
	wantEq(t, "the authenticated mailbox path", path, maildir)
	wrongPath, wrong := d.Authenticate("alice@hermex.test", "wrong")
	wantEq(t, "Authenticate admitted a wrong password", wrong, false)
	wantEq(t, "wrong-password maildir empty", wrongPath, "")
	ghostPath, ghost := d.Authenticate("ghost@hermex.test", "secret-pw1")
	wantEq(t, "Authenticate admitted an unknown user", ghost, false)
	wantEq(t, "ghost-user maildir empty", ghostPath, "")
	// The password is compared exactly: no case folding, no empty match, no prefix.
	for _, bad := range []string{"", "Secret-pw1", "secret-pw", "secret-pw1 "} {
		badPath, ok := d.Authenticate("alice@hermex.test", bad)
		wantEq(t, "Authenticate admitted the password "+bad, ok, false)
		wantEq(t, "bad-password maildir empty for "+bad, badPath, "")
	}

	// Recipient resolution: the user, an alias to the user, and an unknown.
	path, ok = d.Resolve("alice@hermex.test")
	wantEq(t, "Resolve found the user", ok, true)
	wantEq(t, "the resolved mailbox path", path, maildir)
	path, ok = d.Resolve("postmaster@hermex.test")
	wantEq(t, "Resolve followed the alias", ok, true)
	wantEq(t, "the alias's mailbox path", path, maildir)
	unknownPath, unknown := d.Resolve("nobody@hermex.test")
	wantEq(t, "Resolve found an unknown address", unknown, false)
	wantEq(t, "unknown Resolve maildir empty", unknownPath, "")

	// A suspended account (address_status != NORMAL) must not log in.
	execRows(t, "suspend the account", 1)(db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "alice@hermex.test"))
	suspPath, suspended := d.Authenticate("alice@hermex.test", "secret-pw1")
	wantEq(t, "Authenticate admitted a suspended account", suspended, false)
	wantEq(t, "suspended-account maildir empty", suspPath, "")
}

// TestSQLDirectoryIsLocalDomain checks the LocalDomains predicate against the
// domains table: an active domain is local, an unknown domain is not, and a
// suspended domain (domain_status != 0) is treated as non-local so its mail is
// not delivered or looped. Relay routing relies on this to decide deliver vs.
// relay-out.
func TestSQLDirectoryIsLocalDomain(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	did, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test"))
	mustNoErr(t, "create domain", err)
	wantEq(t, "domain id > 0", did > 0, true)

	if ok, err := d.IsLocalDomain("Hermex.Test"); err != nil || !ok {
		t.Errorf("IsLocalDomain(active, mixed case) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := d.IsLocalDomain("gmail.com"); err != nil || ok {
		t.Errorf("IsLocalDomain(unknown) = %v, %v; want false, nil", ok, err)
	}

	// A suspended domain must not be treated as local.
	execRows(t, "suspend hermex.test", 1)(db.Exec(`UPDATE domains SET domain_status = 1 WHERE domainname = ?`, "hermex.test"))
	if ok, err := d.IsLocalDomain("hermex.test"); err != nil || ok {
		t.Errorf("IsLocalDomain(suspended) = %v, %v; want false, nil", ok, err)
	}
}

// TestResolveOpensStoreAcrossPartitions proves mailbox reading is
// partition-agnostic: two users provisioned under two distinct storage roots
// each resolve to their own root, never the other's, and the resolved path
// opens as a real, seeded object store. The directory carries the full maildir
// verbatim, so a mailbox may live on any partition without the read path knowing
// where; an alias chains to the user's one stored path rather than re-deriving a
// default location.
func TestResolveOpensStoreAcrossPartitions(t *testing.T) {
	d := freshSQL(t)
	seedDomain(t, d, t.TempDir(), "hermex.test")

	// Two independent storage roots stand in for two data partitions.
	part0, part1 := t.TempDir(), t.TempDir()
	aliceDir := filepath.Join(part0, "user", "hermex.test", "alice")
	bobDir := filepath.Join(part1, "user", "hermex.test", "bob")
	aID, err := d.CreateUser("alice@hermex.test", "password1", aliceDir)
	mustNoErr(t, "create alice", err)
	bID, err := d.CreateUser("bob@hermex.test", "password1", bobDir)
	mustNoErr(t, "create bob", err)
	wantEq(t, "distinct user ids across partitions", aID > 0 && bID > 0 && aID != bID, true)
	mustNoErr(t, "create the alias", d.CreateAlias("a@hermex.test", "alice@hermex.test"))

	for _, tc := range []struct{ addr, want string }{
		{"alice@hermex.test", aliceDir},
		{"bob@hermex.test", bobDir},
		{"a@hermex.test", aliceDir}, // alias -> alice's partition, not bob's, not a default
	} {
		path, ok := d.Resolve(tc.addr)
		wantEq(t, "Resolve found "+tc.addr, ok, true)
		wantEq(t, "the path "+tc.addr+" resolves to", path, tc.want)
		wantSeededStore(t, path)
	}
}

// TestCreatingAnAccountProvisionsItsMailbox proves the operator's rule: an
// account is usable the moment it is created, so its mailbox exists with the
// standard folders before any mail arrives or anyone signs in. That holds for a
// user (panel and CLI), an LDAP-synced user and a room. A mailbox that cannot be
// created fails the create and leaves no account behind, so a retry can succeed.
func TestCreatingAnAccountProvisionsItsMailbox(t *testing.T) {
	d := freshSQL(t)
	seedDomain(t, d, t.TempDir(), "hermex.test")
	root := t.TempDir()

	aUID, err := d.CreateUser("alice@hermex.test", "password1", filepath.Join(root, "alice"))
	mustNoErr(t, "create alice", err)
	wantEq(t, "alice id > 0", aUID > 0, true)
	bCreated, err := d.UpsertLDAPUser("bob@hermex.test", []byte{0x01}, filepath.Join(root, "bob"))
	mustNoErr(t, "sync bob from LDAP", err)
	wantEq(t, "bob synced as new user", bCreated, true)
	rUID, err := d.CreateRoom("room1@hermex.test", "Room 1", filepath.Join(root, "room1"), 4, false)
	mustNoErr(t, "create the room", err)
	wantEq(t, "room1 id > 0", rUID > 0, true)
	for _, name := range []string{"alice", "bob", "room1"} {
		wantProvisioned(t, filepath.Join(root, name))
	}

	// A path under a regular file cannot become a directory.
	blocker := filepath.Join(root, "blocker")
	mustNoErr(t, "write the blocker file", os.WriteFile(blocker, nil, 0o600))
	failUID, err := d.CreateUser("carol@hermex.test", "password1", filepath.Join(blocker, "carol"))
	if err == nil || failUID != 0 {
		t.Fatalf("creating an account whose mailbox cannot be made returned id=%d err=%v", failUID, err)
	}
	if leftPath, ok := d.Resolve("carol@hermex.test"); ok || leftPath != "" {
		t.Errorf("a failed create left the account behind: %q %v", leftPath, ok)
	}
	retryUID, err := d.CreateUser("carol@hermex.test", "password1", filepath.Join(root, "carol"))
	mustNoErr(t, "retry the create", err)
	wantEq(t, "retried carol id > 0", retryUID > 0, true)
	wantProvisioned(t, filepath.Join(root, "carol"))
}

// TestProvisionMissingMailboxesRepairsOlderAccounts proves the one-off repair the
// operator asked for: an account created before mailboxes were made at account
// creation has a row but no mailbox, and the repair creates exactly those,
// reporting each, while an existing mailbox and its mail are left untouched. A
// second run finds nothing to do.
func TestProvisionMissingMailboxesRepairsOlderAccounts(t *testing.T) {
	d := freshSQL(t)
	seedDomain(t, d, t.TempDir(), "hermex.test")
	root := t.TempDir()
	alice := filepath.Join(root, "alice")
	aliceID, err := d.CreateUser("alice@hermex.test", "password1", alice)
	mustNoErr(t, "create alice", err)
	wantEq(t, "alice id > 0", aliceID > 0, true)
	st, err := objectstore.OpenExisting(alice)
	mustNoErr(t, "open alice", err)
	msg, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: kept\r\n\r\nbody\r\n"), time.Now(), 0)
	mustNoErr(t, "deliver to alice", err)
	wantEq(t, "delivered msg id > 0", msg.ID > 0, true)
	mustNoErr(t, "close alice", st.Close())

	// An account from before the change: a row and nothing on disk.
	old := filepath.Join(root, "old")
	execRows(t, "insert the older account", 1)(d.db.Exec(`INSERT INTO users (username, password, domain_id, homeserver, maildir, lang, timezone, privilege_bits, address_status, display_type)
		SELECT 'old@hermex.test', '', id, 0, ?, '', '', 0, 0, 0 FROM domains WHERE domainname = 'hermex.test'`, old))

	// Two accounts the repair must not write for: one outside every data root,
	// and one under a root that is not present (an unmounted data volume).
	outside := filepath.Join(t.TempDir(), "elsewhere")
	unmounted := filepath.Join(root, "absent-volume", "user", "ghost")
	for _, p := range []struct{ user, dir string }{{"stray@hermex.test", outside}, {"ghost@hermex.test", unmounted}} {
		insertWhat := "insert " + p.user
		execRows(t, insertWhat, 1)(d.db.Exec(`INSERT INTO users (username, password, domain_id, homeserver, maildir, lang, timezone, privilege_bits, address_status, display_type)
			SELECT ?, '', id, 0, ?, '', '', 0, 0, 0 FROM domains WHERE domainname = 'hermex.test'`, p.user, p.dir))
	}

	roots := []string{root, filepath.Join(root, "absent-volume")}
	created, skipped, err := d.ProvisionMissingMailboxes(roots)
	mustNoErr(t, "repair", err)
	if !slices.Equal(created, []string{old}) {
		t.Errorf("repaired %v, want only %v", created, []string{old})
	}
	want := []string{outside, unmounted}
	slices.Sort(skipped)
	slices.Sort(want)
	if !slices.Equal(skipped, want) {
		t.Errorf("skipped %v, want %v", skipped, want)
	}
	for _, p := range []string{outside, unmounted} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the repair wrote at %s: %v", p, err)
		}
	}
	wantProvisioned(t, old)
	st, err = objectstore.OpenExisting(alice)
	mustNoErr(t, "reopen alice", err)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustClose(t, st)
	mustNoErr(t, "list alice's inbox", err)
	wantEq(t, "alice's messages after the repair", len(msgs), 1)

	created, skippedAgain, err := d.ProvisionMissingMailboxes(roots)
	mustNoErr(t, "repair again", err)
	wantEq(t, "mailboxes created by a second run", len(created), 0)
	wantEq(t, "mailboxes skipped on second run", len(skippedAgain), 2)
}

// wantProvisioned proves a mailbox exists without creating it: OpenExisting
// refuses a path no one provisioned, and the standard folders are present.
func wantProvisioned(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.OpenExisting(path)
	mustNoErr(t, "open the provisioned mailbox at "+path, err)
	defer mustClose(t, st)
	for _, fid := range []uint64{mapi.PrivateFIDInbox, mapi.PrivateFIDSentItems, mapi.PrivateFIDDeletedItems, mapi.PrivateFIDCalendar, mapi.PrivateFIDContacts} {
		ok, err := st.FolderExists(int64(fid))
		mustNoErr(t, "look up a standard folder", err)
		if !ok {
			t.Errorf("mailbox %s lacks the standard folder %d", path, fid)
		}
	}
}

// wantSeededStore proves a resolved path opens as a real, initialized object
// store rather than an empty directory.
func wantSeededStore(t *testing.T, path string) {
	t.Helper()
	store, err := objectstore.Open(path)
	mustNoErr(t, "open the store at "+path, err)
	defer mustClose(t, store)
	folders, err := store.ListFolders()
	mustNoErr(t, "list folders in the store at "+path, err)
	if len(folders) == 0 {
		t.Errorf("the store at %q opened with no folders; it was not initialized", path)
	}
}

// mailboxListFixture provisions alice and bob plus a suspended carol, the set
// both mailbox listings are checked against, and returns the two active maildirs.
func mailboxListFixture(t *testing.T) (d *SQLDirectory, aliceDir, bobDir string) {
	t.Helper()
	db := openTestDB(t)
	d = NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	root := t.TempDir()
	if did, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil || did <= 0 {
		t.Fatalf("CreateDomain = %d, %v", did, err)
	}
	aliceDir = filepath.Join(root, "users", "hermex.test", "alice")
	bobDir = filepath.Join(root, "users", "hermex.test", "bob")
	carolDir := filepath.Join(root, "users", "hermex.test", "carol")
	for addr, dir := range map[string]string{
		"alice@hermex.test": aliceDir,
		"bob@hermex.test":   bobDir,
		"carol@hermex.test": carolDir,
	} {
		if uid, err := d.CreateUser(addr, "secret-pw1", dir); err != nil || uid <= 0 {
			t.Fatalf("CreateUser(%s) = %d, %v", addr, uid, err)
		}
	}
	// Suspend carol: a disabled account's Outbox must not be scanned.
	execRows(t, "suspend carol", 1)(db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "carol@hermex.test"))
	return d, aliceDir, bobDir
}

// TestSQLDirectoryMaildirs checks that MailboxLister enumerates the store paths
// of active user mailboxes, the set the send-later spooler scans, and skips a
// suspended account, so the worker never releases mail on a disabled user's
// behalf.
func TestSQLDirectoryMaildirs(t *testing.T) {
	d, aliceDir, bobDir := mailboxListFixture(t)
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

// TestSQLDirectoryMailboxOwners checks the owner listing covers the same mailboxes
// as Maildirs, each with its account, because the send-later spooler sends as the
// account whose Outbox it sweeps.
func TestSQLDirectoryMailboxOwners(t *testing.T) {
	d, aliceDir, bobDir := mailboxListFixture(t)
	owners, err := d.MailboxOwners()
	if err != nil {
		t.Fatal(err)
	}
	want := []MailboxOwner{
		{Address: "alice@hermex.test", StorePath: aliceDir},
		{Address: "bob@hermex.test", StorePath: bobDir},
	}
	if !slices.Equal(owners, want) {
		t.Errorf("MailboxOwners = %v, want %v", owners, want)
	}
}

// TestSQLDirectorySharedMailboxes checks the shared-mailbox enumerator: only an
// account carrying the shared status bit in an active domain is returned (with
// its address and store path), a normal mailbox is excluded, and a shared
// mailbox in a disabled domain is excluded by the domain join.
func TestSQLDirectorySharedMailboxes(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedDomain(t, d, root, "old.test")
	supportDir := filepath.Join(root, "users", "support")
	for addr, dir := range map[string]string{
		// alice is a normal mailbox (not shared), support is shared in an active
		// domain, and archive is shared but its domain gets disabled below.
		"alice@hermex.test":   filepath.Join(root, "users", "alice"),
		"support@hermex.test": supportDir,
		"archive@old.test":    filepath.Join(root, "users", "archive"),
	} {
		uid, err := d.CreateUser(addr, "secret-pw1", dir)
		mustNoErr(t, "create "+addr, err)
		wantEq(t, "user id > 0 for "+addr, uid > 0, true)
	}
	// Flag the two shared mailboxes, then disable the old.test domain.
	execRows(t, "flag the shared mailboxes", 2)(db.Exec(`UPDATE users SET address_status = ? WHERE username IN (?, ?)`,
		afUserSharedMbox, "support@hermex.test", "archive@old.test"))
	execRows(t, "disable the old domain", 1)(db.Exec(`UPDATE domains SET domain_status = 1 WHERE domainname = ?`, "old.test"))

	got, err := d.SharedMailboxes("alice@hermex.test")
	mustNoErr(t, "list shared mailboxes", err)
	if len(got) != 1 {
		t.Fatalf("SharedMailboxes = %v, want only the active-domain shared mailbox (the normal user and the disabled domain's are excluded)", got)
	}
	wantEq(t, "the shared mailbox", got[0], SharedMailbox{Address: "support@hermex.test", StorePath: supportDir})
}

// TestSQLDirectorySearchGAL checks GAL recipient search over the SQL directory:
// a case-insensitive substring match on the usernames of active mailbox users,
// excluding a suspended account, ordered by address, with the result cap honored,
// the display name taken from PR_DISPLAY_NAME in user_properties, and the address
// used as the fallback when no display name is set.
func TestSQLDirectorySearchGAL(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	for _, u := range []string{"alice@hermex.test", "albert@hermex.test", "bob@hermex.test"} {
		seedUser(t, d, root, u, "secret-pw1")
	}
	// Suspend albert: a disabled account must not surface in the address list.
	execRows(t, "suspend albert", 1)(db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "albert@hermex.test"))
	// Give bob a PR_DISPLAY_NAME so the GAL returns the name, not the address;
	// alice keeps none, exercising the address fallback.
	ok, err := d.SetUserProperties("bob@hermex.test", map[uint32]string{0x3001001F: "Bob Builder"})
	mustNoErr(t, "set bob's display name", err)
	wantEq(t, "set bob display name found", ok, true)

	// "al" substring-matches alice and albert, but albert is suspended, so only
	// alice remains. The query is case-insensitive.
	for _, q := range []string{"al", "AL"} {
		got := mustSearchGAL(t, d, q, 0)
		if len(got) != 1 {
			t.Fatalf("SearchGAL(%q) = %v, want [alice@hermex.test] (albert is suspended)", q, got)
		}
		wantEq(t, "matched address", got[0].Address, "alice@hermex.test")
		wantEq(t, "display name falls back to the address", got[0].DisplayName, got[0].Address)
	}

	// A domain-wide query returns every active user ordered by address.
	all := mustSearchGAL(t, d, "hermex.test", 0)
	if len(all) != 2 {
		t.Fatalf("SearchGAL(domain) = %v, want alice and bob (albert is suspended)", all)
	}
	wantEq(t, "first address (ordered)", all[0].Address, "alice@hermex.test")
	wantEq(t, "second address (ordered)", all[1].Address, "bob@hermex.test")

	// The limit caps the result count.
	wantEq(t, "results under a limit of 1", len(mustSearchGAL(t, d, "hermex.test", 1)), 1)

	// bob's PR_DISPLAY_NAME surfaces as the display name (the LEFT JOIN), while
	// alice with none keeps the address fallback asserted above.
	bob := mustSearchGAL(t, d, "bob", 0)
	if len(bob) != 1 {
		t.Fatalf("SearchGAL(bob) = %v, want one entry", bob)
	}
	wantEq(t, "bob's address", bob[0].Address, "bob@hermex.test")
	wantEq(t, "bob's display name", bob[0].DisplayName, "Bob Builder")

	// LIKE metacharacters in a query match themselves only: "%" and "_" must not
	// turn a search into a listing of the whole address book.
	for _, q := range []string{"%", "_", "a%e", "b_b"} {
		wantEq(t, "results for the wildcard query "+q, len(mustSearchGAL(t, d, q, 0)), 0)
	}
}

// mustSearchGAL runs a GAL search as alice.
func mustSearchGAL(t *testing.T, d *SQLDirectory, query string, limit int) []GALEntry {
	t.Helper()
	got, err := d.SearchGAL("alice@hermex.test", query, limit, 0)
	mustNoErr(t, "search GAL", err)
	return got
}
