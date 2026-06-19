package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAdminListUsers proves a system admin lists every user.
func TestAdminListUsers(t *testing.T) {
	d := &fakeDir{
		authOK: true,
		uid:    7,
		roles:  []directory.AdminRole{{Role: directory.AdminSystem}},
		users:  []directory.UserInfo{{ID: 1, Username: "boss@hermex.test", DomainID: 1}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list users status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "boss@hermex.test") {
		t.Errorf("list body = %s, want the user", body)
	}
}

// TestAdminListUsersRequiresSystem proves a domain admin (not a system admin) is
// refused the global user list.
func TestAdminListUsersRequiresSystem(t *testing.T) {
	d := &fakeDir{
		authOK: true,
		uid:    7,
		roles:  []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin list users = %d, want 403", resp.StatusCode)
	}
}

// TestAdminCreateUser proves a system admin provisions a user whose maildir is
// derived through the Paths deriver.
func TestAdminCreateUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"new@hermex.test","password":"pw"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user status %d, want 201", resp.StatusCode)
	}
	if d.createdUser != "new@hermex.test" {
		t.Errorf("created user %q, want new@hermex.test", d.createdUser)
	}
	if !strings.HasSuffix(d.createdMaildir, "/mbox/new@hermex.test") {
		t.Errorf("maildir %q not derived through Paths", d.createdMaildir)
	}
}

// TestAdminCreateUserValidates proves a request missing the password is refused
// before the directory is touched.
func TestAdminCreateUserValidates(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"new@hermex.test"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing-password status %d, want 400", resp.StatusCode)
	}
	if d.createdUser != "" {
		t.Errorf("an invalid request still provisioned %q", d.createdUser)
	}
}

// TestAdminSetPassword proves a system admin resets a user's password.
func TestAdminSetPassword(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/boss@hermex.test/password", session, csrf, `{"password":"newpass"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set password status %d, want 204", resp.StatusCode)
	}
	if d.setPwUser != "boss@hermex.test" || d.setPwValue != "newpass" {
		t.Errorf("set password for %q = %q, want boss@hermex.test = newpass", d.setPwUser, d.setPwValue)
	}
}

// TestAdminSetPasswordNotFound proves resetting an unknown user's password is a
// 404.
func TestAdminSetPasswordNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:        []directory.AdminRole{{Role: directory.AdminSystem}},
		setPwMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/ghost@hermex.test/password", session, csrf, `{"password":"x"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("set password for unknown user = %d, want 404", resp.StatusCode)
	}
}
