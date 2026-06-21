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
	for _, tbl := range []string{"altnames", "aliases", "forwards", "fetchmail_seen", "fetchmail", "admin_roles", "user_roles", "role_permissions", "roles", "associations", "specifieds", "mlists", "users", "domains", "orgs", "ldap_config", "sync_policy", "create_defaults", "active_sessions"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}
}

// TestForwardDirective covers the forwards model: a directive set on a user is read
// back, an alias to that user resolves to the same directive (canonical keying — mail
// to an alias must not bypass the forward), clearing removes it, an unknown user is
// reported absent, and DeleteUser leaves no orphan forward row.
func TestForwardDirective(t *testing.T) {
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
	if _, err := d.CreateUser("alice@hermex.test", "secret", filepath.Join(root, "alice")); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("sales@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}

	// Set a Redirect and read it back by canonical username.
	if existed, err := d.SetForward("alice@hermex.test", ForwardRedirect, "boss@external.test"); err != nil || !existed {
		t.Fatalf("SetForward = %v, %v; want true, nil", existed, err)
	}
	if fi, ok, err := d.GetForward("alice@hermex.test"); err != nil || !ok || fi.Type != ForwardRedirect || fi.Destination != "boss@external.test" {
		t.Errorf("GetForward(user) = %+v, %v, %v; want {Redirect boss@external.test}, true", fi, ok, err)
	}
	// An alias to the user must resolve to the same directive — keying on the raw
	// alias would let mail to sales@ bypass alice's forward.
	if fi, ok, err := d.GetForward("sales@hermex.test"); err != nil || !ok || fi.Destination != "boss@external.test" {
		t.Errorf("GetForward(alias) = %+v, %v, %v; want the user's directive", fi, ok, err)
	}

	// An empty destination clears the forward.
	if existed, err := d.SetForward("alice@hermex.test", ForwardCC, ""); err != nil || !existed {
		t.Fatalf("SetForward(clear) = %v, %v; want true, nil", existed, err)
	}
	if _, ok, err := d.GetForward("alice@hermex.test"); err != nil || ok {
		t.Errorf("GetForward after clear = ok %v, %v; want false", ok, err)
	}

	// An unknown user is reported absent, not created.
	if existed, err := d.SetForward("ghost@hermex.test", ForwardCC, "x@y.test"); err != nil || existed {
		t.Errorf("SetForward(unknown) = %v, %v; want false, nil", existed, err)
	}

	// DeleteUser leaves no orphan forward row.
	if _, err := d.SetForward("alice@hermex.test", ForwardCC, "boss@external.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeleteUser("alice@hermex.test", false); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forwards WHERE username = ?`, "alice@hermex.test").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("forwards rows after DeleteUser = %d, want 0", n)
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
// each resolve to their own root — never the other's — and the resolved path
// opens as a real, seeded object store. The directory carries the full maildir
// verbatim, so a mailbox may live on any partition without the read path knowing
// where; an alias chains to the user's one stored path rather than re-deriving a
// default location.
func TestResolveOpensStoreAcrossPartitions(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	if _, err := d.CreateDomain("hermex.test", filepath.Join(t.TempDir(), "domains", "hermex.test")); err != nil {
		t.Fatal(err)
	}

	// Two independent storage roots stand in for two data partitions.
	part0, part1 := t.TempDir(), t.TempDir()
	aliceDir := filepath.Join(part0, "user", "hermex.test", "alice")
	bobDir := filepath.Join(part1, "user", "hermex.test", "bob")
	if _, err := d.CreateUser("alice@hermex.test", "pw", aliceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("bob@hermex.test", "pw", bobDir); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAlias("a@hermex.test", "alice@hermex.test"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ addr, want string }{
		{"alice@hermex.test", aliceDir},
		{"bob@hermex.test", bobDir},
		{"a@hermex.test", aliceDir}, // alias -> alice's partition, not bob's, not a default
	} {
		path, ok := d.Resolve(tc.addr)
		if !ok || path != tc.want {
			t.Fatalf("Resolve(%q) = %q, %v; want %q, true", tc.addr, path, ok, tc.want)
		}
		store, err := objectstore.Open(path)
		if err != nil {
			t.Fatalf("objectstore.Open(%q): %v", path, err)
		}
		folders, err := store.ListFolders()
		store.Close()
		if err != nil {
			t.Fatalf("ListFolders on the store at %q: %v", path, err)
		}
		if len(folders) == 0 {
			t.Errorf("store at %q opened with no folders; it was not initialized", path)
		}
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

// TestSQLDirectorySearchGAL checks GAL recipient search over the SQL directory:
// a case-insensitive substring match on the usernames of active mailbox users,
// excluding a suspended account, ordered by address, with the result cap honored,
// the display name taken from PR_DISPLAY_NAME in user_properties, and the address
// used as the fallback when no display name is set.
func TestSQLDirectorySearchGAL(t *testing.T) {
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
	for _, u := range []string{"alice@hermex.test", "albert@hermex.test", "bob@hermex.test"} {
		if _, err := d.CreateUser(u, "secret", filepath.Join(root, "users", u)); err != nil {
			t.Fatal(err)
		}
	}
	// Suspend albert: a disabled account must not surface in the address list.
	if _, err := db.Exec(`UPDATE users SET address_status = ? WHERE username = ?`, afUserSuspended, "albert@hermex.test"); err != nil {
		t.Fatal(err)
	}
	// Give bob a PR_DISPLAY_NAME so the GAL returns the name, not the address;
	// alice keeps none, exercising the address fallback.
	if _, err := d.SetUserProperties("bob@hermex.test", map[uint32]string{0x3001001F: "Bob Builder"}); err != nil {
		t.Fatal(err)
	}

	// "al" substring-matches alice and albert, but albert is suspended, so only
	// alice remains. The query is case-insensitive.
	for _, q := range []string{"al", "AL"} {
		got, err := d.SearchGAL(q, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Address != "alice@hermex.test" {
			t.Errorf("SearchGAL(%q) = %v, want [alice@hermex.test] (albert is suspended)", q, got)
		} else if got[0].DisplayName != got[0].Address {
			t.Errorf("DisplayName %q should mirror Address %q", got[0].DisplayName, got[0].Address)
		}
	}

	// A domain-wide query returns every active user ordered by address.
	all, err := d.SearchGAL("hermex.test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("SearchGAL(domain) = %v, want alice and bob (albert is suspended)", all)
	}
	if all[0].Address != "alice@hermex.test" || all[1].Address != "bob@hermex.test" {
		t.Errorf("SearchGAL(domain) = %v, want ordered [alice, bob]", all)
	}

	// The limit caps the result count.
	if got, _ := d.SearchGAL("hermex.test", 1); len(got) != 1 {
		t.Errorf("SearchGAL(domain, limit 1) returned %d, want 1", len(got))
	}

	// bob's PR_DISPLAY_NAME surfaces as the display name (the LEFT JOIN), while
	// alice with none keeps the address fallback asserted above.
	bob, _ := d.SearchGAL("bob", 0)
	if len(bob) != 1 || bob[0].Address != "bob@hermex.test" || bob[0].DisplayName != "Bob Builder" {
		t.Errorf("SearchGAL(bob) = %v, want bob@hermex.test displayed as %q", bob, "Bob Builder")
	}
}
