package admin

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"hermex/internal/directory"
)

// openTestDB connects to HERMEX_TEST_MYSQL_DSN, creating the test database on
// demand, and skips when the DSN is unset.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HERMEX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("HERMEX_TEST_MYSQL_DSN not set; skipping MariaDB admin integration test")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse HERMEX_TEST_MYSQL_DSN: %v", err)
	}
	// Use a database distinct from the directory package's shared test DB: Go runs
	// the two packages' tests concurrently, so sharing the users/domains tables
	// would let one package's cleanup delete the other's rows mid-test.
	dbName := cfg.DBName + "_admin"
	cfg.DBName = ""
	bootDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	var pingErr error
	for range 30 {
		if pingErr = bootDB.Ping(); pingErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		bootDB.Close()
		t.Fatalf("ping: %v", pingErr)
	}
	if _, err := bootDB.Exec("CREATE DATABASE IF NOT EXISTS `" + dbName + "`"); err != nil {
		bootDB.Close()
		t.Fatalf("create test database %q: %v", dbName, err)
	}
	bootDB.Close()

	cfg.DBName = dbName
	db, err := sql.Open("mysql", cfg.FormatDSN())
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

// TestAdminServerIntegration exercises the admin server against a real
// SQLDirectory on MariaDB: it provisions a domain, an admin user, and a system
// role, then proves login, whoami, and the domain listing work end-to-end (and
// a wrong password is refused against the stored hash).
func TestAdminServerIntegration(t *testing.T) {
	db := openTestDB(t)
	dir := directory.NewSQL(db)
	if err := dir.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"altnames", "aliases", "admin_roles", "users", "domains"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}

	root := t.TempDir()
	if _, err := dir.CreateDomain("hermex.test", root+"/dom"); err != nil {
		t.Fatal(err)
	}
	uid, err := dir.CreateUser("boss@hermex.test", "s3cret", root+"/boss")
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.GrantAdminRole(uid, directory.AdminSystem, 0); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(NewServer(dir, fakePaths{root: root}, []byte("integration-secret")).Handler())
	t.Cleanup(ts.Close)

	// 1. Login with the real credentials issues a session cookie.
	resp, err := http.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"boss@hermex.test","password":"s3cret"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status %d, want 200", resp.StatusCode)
	}
	var session, csrf string
	for _, sc := range resp.Header["Set-Cookie"] {
		if strings.HasPrefix(sc, sessionCookie+"=") {
			session = cookieValue(sc, sessionCookie)
		}
		if strings.HasPrefix(sc, csrfCookie+"=") {
			csrf = cookieValue(sc, csrfCookie)
		}
	}
	if session == "" || csrf == "" {
		t.Fatal("login set no session/CSRF cookie")
	}

	// 2. whoami reports the real identity and system role.
	who := authedGET(t, ts, "/admin/whoami", session)
	defer who.Body.Close()
	if who.StatusCode != http.StatusOK {
		t.Fatalf("whoami status %d, want 200", who.StatusCode)
	}
	whoBody, _ := io.ReadAll(who.Body)
	if !strings.Contains(string(whoBody), "boss@hermex.test") || !strings.Contains(string(whoBody), "system") {
		t.Errorf("whoami body = %s, want the login and the system role", whoBody)
	}

	// 3. The domain listing returns the provisioned domain.
	dom := authedGET(t, ts, "/admin/domains", session)
	defer dom.Body.Close()
	if dom.StatusCode != http.StatusOK {
		t.Fatalf("domains status %d, want 200", dom.StatusCode)
	}
	domBody, _ := io.ReadAll(dom.Body)
	if !strings.Contains(string(domBody), "hermex.test") {
		t.Errorf("domains body = %s, want hermex.test", domBody)
	}

	// 4. The user listing returns the provisioned admin account.
	usr := authedGET(t, ts, "/admin/users", session)
	defer usr.Body.Close()
	if usr.StatusCode != http.StatusOK {
		t.Fatalf("users status %d, want 200", usr.StatusCode)
	}
	usrBody, _ := io.ReadAll(usr.Body)
	if !strings.Contains(string(usrBody), "boss@hermex.test") {
		t.Errorf("users body = %s, want boss@hermex.test", usrBody)
	}

	// 5. A wrong password is refused against the stored hash.
	bad, err := http.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"boss@hermex.test","password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-password login = %d, want 401", bad.StatusCode)
	}

	// 6. Provision a new user through the API (a state-changing request with CSRF)
	// and confirm it lands in the directory — proving the create path and the
	// config-derived maildir end-to-end.
	cr := authedPOST(t, ts, "/admin/users", session, csrf,
		`{"email":"intern@hermex.test","password":"pw2"}`)
	cr.Body.Close()
	if cr.StatusCode != http.StatusCreated {
		t.Fatalf("API create-user status %d, want 201", cr.StatusCode)
	}
	if _, ok, _ := dir.UserID("intern@hermex.test"); !ok {
		t.Error("the API-created user did not land in the directory")
	}

	// 7. Set then read an org's LDAP config through the API (real ldap_config
	// table); the read must not echo the bind password.
	put := authedPUT(t, ts, "/admin/orgs/5/ldap", session, csrf,
		`{"URI":"ldaps://dc.hermex.test","BindDN":"cn=svc","BindPassword":"topsecret","BaseDN":"dc=hermex,dc=test"}`)
	put.Body.Close()
	if put.StatusCode != http.StatusNoContent {
		t.Fatalf("put ldap status %d, want 204", put.StatusCode)
	}
	got := authedGET(t, ts, "/admin/orgs/5/ldap", session)
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get ldap status %d, want 200", got.StatusCode)
	}
	ldapBody, _ := io.ReadAll(got.Body)
	if !strings.Contains(string(ldapBody), "ldaps://dc.hermex.test") {
		t.Errorf("ldap body = %s, want the stored URI", ldapBody)
	}
	if strings.Contains(string(ldapBody), "topsecret") {
		t.Errorf("the bind password leaked from the real config: %s", ldapBody)
	}

	// 8. Reset the new user's password through the API; the new password must
	// authenticate against the real hash and the old one must not.
	pwReset := authedPOST(t, ts, "/admin/users/intern@hermex.test/password", session, csrf,
		`{"password":"pw3"}`)
	pwReset.Body.Close()
	if pwReset.StatusCode != http.StatusNoContent {
		t.Fatalf("password reset status %d, want 204", pwReset.StatusCode)
	}
	if _, ok := dir.Authenticate("intern@hermex.test", "pw3"); !ok {
		t.Error("the API-reset password does not authenticate")
	}
	if _, ok := dir.Authenticate("intern@hermex.test", "pw2"); ok {
		t.Error("the old password still authenticates after a reset")
	}
}
