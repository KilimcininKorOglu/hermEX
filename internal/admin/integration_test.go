package admin

import (
	"database/sql"
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
	dir := seedIntegrationDirectory(t)
	root := t.TempDir()
	ts := httptest.NewServer(NewServer(dir, fakePaths{root: root}, []byte("integration-secret")).Handler())
	t.Cleanup(ts.Close)

	session, csrf := loginBoss(t, ts)

	// whoami reports the real identity and system role.
	who := wantBody(t, authedGET(t, ts, "/admin/whoami", session), http.StatusOK, "whoami")
	wantContains(t, who, "boss@hermex.test", "whoami reports the login")
	wantContains(t, who, "system", "whoami reports the system role")

	// The listings return what was provisioned.
	dom := wantBody(t, authedGET(t, ts, "/admin/domains", session), http.StatusOK, "domains")
	wantContains(t, dom, "hermex.test", "the domain listing carries the provisioned domain")
	usr := wantBody(t, authedGET(t, ts, "/admin/users", session), http.StatusOK, "users")
	wantContains(t, usr, "boss@hermex.test", "the user listing carries the admin account")

	// A wrong password is refused against the stored hash.
	wantStatus(t, postBossLogin(t, ts, "wrong"), http.StatusUnauthorized, "wrong-password login")

	checkAPICreateUser(t, ts, dir, session, csrf)
	checkLDAPConfigRoundTrip(t, ts, session, csrf)
	checkPasswordReset(t, ts, dir, session, csrf)
	checkRoleGrantRevoke(t, ts, dir, session, csrf)
}

// seedIntegrationDirectory prepares a clean directory holding one domain and one
// system-admin account.
func seedIntegrationDirectory(t *testing.T) *directory.SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	dir := directory.NewSQL(db)
	mustNoErr(t, dir.EnsureSchema(), "ensure schema")
	for _, tbl := range []string{"altnames", "aliases", "admin_roles", "users", "domains"} {
		_, err := db.Exec("DELETE FROM " + tbl)
		mustNoErr(t, err, "clean "+tbl)
	}
	root := t.TempDir()
	_, err := dir.CreateDomain("hermex.test", root+"/dom")
	mustNoErr(t, err, "create domain")
	uid, err := dir.CreateUser("boss@hermex.test", "s3cret", root+"/boss")
	mustNoErr(t, err, "create admin user")
	mustNoErr(t, dir.GrantAdminRole(uid, directory.AdminSystem, 0), "grant system role")
	return dir
}

// postBossLogin submits the admin login form for the seeded account.
func postBossLogin(t *testing.T, ts *httptest.Server, password string) *http.Response {
	t.Helper()
	resp, err := http.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"boss@hermex.test","password":"`+password+`"}`))
	mustNoErr(t, err, "post login")
	return resp
}

// loginBoss logs in with the real credentials and returns the issued session and
// CSRF cookie values.
func loginBoss(t *testing.T, ts *httptest.Server) (session, csrf string) {
	t.Helper()
	resp := postBossLogin(t, ts, "s3cret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status %d, want 200", resp.StatusCode)
	}
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
	return session, csrf
}

// checkAPICreateUser provisions a user through the API (a state-changing request
// with CSRF) and confirms it lands in the directory, proving the create path and
// the config-derived maildir end-to-end.
func checkAPICreateUser(t *testing.T, ts *httptest.Server, dir *directory.SQLDirectory, session, csrf string) {
	t.Helper()
	cr := authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"intern@hermex.test","password":"pw2"}`)
	wantStatus(t, cr, http.StatusCreated, "API create-user")
	_, ok, _ := dir.UserID("intern@hermex.test")
	wantTrue(t, ok, "the API-created user lands in the directory")
}

// checkLDAPConfigRoundTrip sets then reads an org's LDAP config through the API
// (real ldap_config table); the read must not echo the bind password.
func checkLDAPConfigRoundTrip(t *testing.T, ts *httptest.Server, session, csrf string) {
	t.Helper()
	put := authedPUT(t, ts, "/admin/orgs/5/ldap", session, csrf,
		`{"URI":"ldaps://dc.hermex.test","BindDN":"cn=svc","BindPassword":"topsecret","BaseDN":"dc=hermex,dc=test"}`)
	wantStatus(t, put, http.StatusNoContent, "put ldap")
	body := wantBody(t, authedGET(t, ts, "/admin/orgs/5/ldap", session), http.StatusOK, "get ldap")
	wantContains(t, body, "ldaps://dc.hermex.test", "the read carries the stored URI")
	wantNotContains(t, body, "topsecret", "the bind password stays out of the read")
}

// checkPasswordReset resets the new user's password through the API. The reset
// stores the new hash and flags the account for a forced change: the strict
// Authenticate denies a flagged account, so the stored hash is verified through the
// lenient path, and the strict path must refuse even the correct password. That
// refusal is what locks the temporary password out of every client protocol until
// the user changes it.
func checkPasswordReset(t *testing.T, ts *httptest.Server, dir *directory.SQLDirectory, session, csrf string) {
	t.Helper()
	reset := authedPOST(t, ts, "/admin/users/intern@hermex.test/password", session, csrf, `{"password":"pw3"}`)
	wantStatus(t, reset, http.StatusNoContent, "password reset")

	_, lenient := dir.AuthenticateAllowingPasswordChange("intern@hermex.test", "pw3")
	wantTrue(t, lenient, "the reset password authenticates through the lenient path")
	_, strict := dir.Authenticate("intern@hermex.test", "pw3")
	wantFalse(t, strict, "the strict path denies a must-change account")
	_, old := dir.Authenticate("intern@hermex.test", "pw2")
	wantFalse(t, old, "the old password stops authenticating after a reset")

	u, found, _ := dir.GetUser("intern@hermex.test")
	wantTrue(t, found && u.MustChangePassword, "an admin reset sets must_change_password")
}

// checkRoleGrantRevoke grants then revokes an admin role through the API against
// the real admin_roles table, and confirms an unknown role tier is rejected.
func checkRoleGrantRevoke(t *testing.T, ts *httptest.Server, dir *directory.SQLDirectory, session, csrf string) {
	t.Helper()
	internUID, ok, _ := dir.UserID("intern@hermex.test")
	if !ok {
		t.Fatal("the intern user vanished")
	}
	grant := authedPOST(t, ts, "/admin/users/intern@hermex.test/roles", session, csrf, `{"role":"org","scopeID":5}`)
	wantStatus(t, grant, http.StatusNoContent, "grant role")
	granted, _ := dir.AdminRoles(internUID)
	wantEq(t, len(granted), 1, "role count after grant")
	wantTrue(t, granted[0].Role == directory.AdminOrg && granted[0].ScopeID == 5, "the granted role is org:5")

	revoke := authedDELETE(t, ts, "/admin/users/intern@hermex.test/roles", session, csrf, `{"role":"org","scopeID":5}`)
	wantStatus(t, revoke, http.StatusNoContent, "revoke role")
	left, _ := dir.AdminRoles(internUID)
	wantEq(t, len(left), 0, "role count after revoke")

	badRole := authedPOST(t, ts, "/admin/users/intern@hermex.test/roles", session, csrf, `{"role":"wizard"}`)
	wantStatus(t, badRole, http.StatusBadRequest, "granting an unknown role tier")
}
