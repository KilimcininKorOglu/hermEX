package directory

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

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
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	// MariaDB may still be starting; ping with a bounded retry.
	var pingErr error
	for range 30 {
		if pingErr = admin.Ping(); pingErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		admin.Close()
		t.Fatalf("ping: %v", pingErr)
	}
	if _, err := admin.Exec("CREATE DATABASE IF NOT EXISTS `" + dbName + "`"); err != nil {
		admin.Close()
		t.Fatalf("create test database %q: %v", dbName, err)
	}
	admin.Close()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping %q: %v", dbName, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func cleanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, tbl := range []string{"app_passwords", "user_totp_recovery", "user_totp", "altnames", "aliases", "forwards", "fetchmail_seen", "fetchmail", "admin_roles", "user_roles", "role_permissions", "roles", "associations", "specifieds", "mlists", "users", "domains", "orgs", "ldap_config", "sync_policy", "create_defaults", "active_sessions", "webmail_sessions", "admin_sessions", "push_subscriptions", "dkim_keys", "tls_certs"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
}

// TestForwardDirective covers the forwards model: a directive set on a user is read
// back, an alias to that user resolves to the same directive (canonical keying, mail
// to an alias must not bypass the forward), clearing removes it, an unknown user is
// reported absent, and DeleteUser leaves no orphan forward row.
func TestForwardDirective(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "secret")
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

	// An empty destination clears the forward.
	existed, err = d.SetForward("alice@hermex.test", ForwardCC, "")
	mustNoErr(t, "clear forward", err)
	wantEq(t, "SetForward(clear) found the user", existed, true)
	_, ok, err := d.GetForward("alice@hermex.test")
	mustNoErr(t, "get forward", err)
	wantEq(t, "forward present after clearing", ok, false)

	// An unknown user is reported absent, not created.
	existed, err = d.SetForward("ghost@hermex.test", ForwardCC, "x@y.test")
	mustNoErr(t, "set a forward on an unknown user", err)
	wantEq(t, "SetForward(unknown) found a user", existed, false)

	// DeleteUser leaves no orphan forward row.
	_, err = d.SetForward("alice@hermex.test", ForwardCC, "boss@external.test")
	mustNoErr(t, "set forward", err)
	_, err = d.DeleteUser("alice@hermex.test", false)
	mustNoErr(t, "delete user", err)
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
	mustCreateDomain(t, d, root, "hermex.test")
	maildir := filepath.Join(root, "users", "hermex.test", "alice")
	_, err := d.CreateUser("Alice@Hermex.Test", "secret", maildir)
	mustNoErr(t, "create user", err)
	mustNoErr(t, "create alias", d.CreateAlias("postmaster@hermex.test", "alice@hermex.test"))

	// Resolution yields the maildir itself: that is the path handed to
	// objectstore.Open, which opens objects.sqlite3 + imapindex.sqlite3 inside it.

	// Authentication: correct password (case-insensitive login), wrong password,
	// and unknown user.
	path, ok := d.Authenticate("alice@hermex.test", "secret")
	wantEq(t, "Authenticate admitted the correct password", ok, true)
	wantEq(t, "the authenticated mailbox path", path, maildir)
	_, wrong := d.Authenticate("alice@hermex.test", "wrong")
	wantEq(t, "Authenticate admitted a wrong password", wrong, false)
	_, ghost := d.Authenticate("ghost@hermex.test", "secret")
	wantEq(t, "Authenticate admitted an unknown user", ghost, false)

	// Recipient resolution: the user, an alias to the user, and an unknown.
	path, ok = d.Resolve("alice@hermex.test")
	wantEq(t, "Resolve found the user", ok, true)
	wantEq(t, "the resolved mailbox path", path, maildir)
	path, ok = d.Resolve("postmaster@hermex.test")
	wantEq(t, "Resolve followed the alias", ok, true)
	wantEq(t, "the alias's mailbox path", path, maildir)
	_, unknown := d.Resolve("nobody@hermex.test")
	wantEq(t, "Resolve found an unknown address", unknown, false)

	// A suspended account (address_status != NORMAL) must not log in.
	_, err = db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "alice@hermex.test")
	mustNoErr(t, "suspend the account", err)
	_, suspended := d.Authenticate("alice@hermex.test", "secret")
	wantEq(t, "Authenticate admitted a suspended account", suspended, false)
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
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil {
		t.Fatal(err)
	}

	if ok, err := d.IsLocalDomain("Hermex.Test"); err != nil || !ok {
		t.Errorf("IsLocalDomain(active, mixed case) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := d.IsLocalDomain("gmail.com"); err != nil || ok {
		t.Errorf("IsLocalDomain(unknown) = %v, %v; want false, nil", ok, err)
	}

	// A suspended domain must not be treated as local.
	if _, err := db.Exec(`UPDATE domains SET domain_status = 1 WHERE domainname = ?`, "hermex.test"); err != nil {
		t.Fatal(err)
	}
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
	d, _ := freshDirectory(t)
	mustCreateDomain(t, d, t.TempDir(), "hermex.test")

	// Two independent storage roots stand in for two data partitions.
	part0, part1 := t.TempDir(), t.TempDir()
	aliceDir := filepath.Join(part0, "user", "hermex.test", "alice")
	bobDir := filepath.Join(part1, "user", "hermex.test", "bob")
	_, err := d.CreateUser("alice@hermex.test", "pw", aliceDir)
	mustNoErr(t, "create alice", err)
	_, err = d.CreateUser("bob@hermex.test", "pw", bobDir)
	mustNoErr(t, "create bob", err)
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

// wantSeededStore proves a resolved path opens as a real, initialized object
// store rather than an empty directory.
func wantSeededStore(t *testing.T, path string) {
	t.Helper()
	store, err := objectstore.Open(path)
	mustNoErr(t, "open the store at "+path, err)
	defer store.Close()
	folders, err := store.ListFolders()
	mustNoErr(t, "list folders in the store at "+path, err)
	if len(folders) == 0 {
		t.Errorf("the store at %q opened with no folders; it was not initialized", path)
	}
}

// TestSQLDirectoryMaildirs checks that MailboxLister enumerates the store paths
// of active user mailboxes, the set the send-later spooler scans, and skips a
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

// TestSQLDirectorySharedMailboxes checks the shared-mailbox enumerator: only an
// account carrying the shared status bit in an active domain is returned (with
// its address and store path), a normal mailbox is excluded, and a shared
// mailbox in a disabled domain is excluded by the domain join.
func TestSQLDirectorySharedMailboxes(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateDomain(t, d, root, "old.test")
	supportDir := filepath.Join(root, "users", "support")
	for addr, dir := range map[string]string{
		// alice is a normal mailbox (not shared), support is shared in an active
		// domain, and archive is shared but its domain gets disabled below.
		"alice@hermex.test":   filepath.Join(root, "users", "alice"),
		"support@hermex.test": supportDir,
		"archive@old.test":    filepath.Join(root, "users", "archive"),
	} {
		_, err := d.CreateUser(addr, "secret", dir)
		mustNoErr(t, "create "+addr, err)
	}
	// Flag the two shared mailboxes, then disable the old.test domain.
	_, err := db.Exec(`UPDATE users SET address_status = ? WHERE username IN (?, ?)`,
		afUserSharedMbox, "support@hermex.test", "archive@old.test")
	mustNoErr(t, "flag the shared mailboxes", err)
	_, err = db.Exec(`UPDATE domains SET domain_status = 1 WHERE domainname = ?`, "old.test")
	mustNoErr(t, "disable the old domain", err)

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
	mustCreateDomain(t, d, root, "hermex.test")
	for _, u := range []string{"alice@hermex.test", "albert@hermex.test", "bob@hermex.test"} {
		mustCreateUser(t, d, root, u, "secret")
	}
	// Suspend albert: a disabled account must not surface in the address list.
	_, err := db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "albert@hermex.test")
	mustNoErr(t, "suspend albert", err)
	// Give bob a PR_DISPLAY_NAME so the GAL returns the name, not the address;
	// alice keeps none, exercising the address fallback.
	_, err = d.SetUserProperties("bob@hermex.test", map[uint32]string{0x3001001F: "Bob Builder"})
	mustNoErr(t, "set bob's display name", err)

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
}

// mustSearchGAL runs a GAL search as alice.
func mustSearchGAL(t *testing.T, d *SQLDirectory, query string, limit int) []GALEntry {
	t.Helper()
	got, err := d.SearchGAL("alice@hermex.test", query, limit)
	mustNoErr(t, "search GAL", err)
	return got
}
