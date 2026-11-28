package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// authedDELETE issues a DELETE carrying the session and CSRF cookies plus the
// CSRF header.
func authedDELETE(t *testing.T, ts *httptest.Server, path, session, csrf, body string) *http.Response {
	t.Helper()
	req := newRequest(t, "DELETE", ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	req.Header.Set(csrfHeader, csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestAdminListRoles proves a system admin lists a user's roles.
func TestAdminListRoles(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminSystem}, {Role: directory.AdminOrg, ScopeID: 3}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	wantStatus(t, authedGET(t, ts, "/admin/users/x@hermex.test/roles", ""), http.StatusUnauthorized, "unauth list roles")
	body := wantBody(t, authedGET(t, ts, "/admin/users/x@hermex.test/roles", session), http.StatusOK, "list roles")
	if !strings.Contains(body, "system") || !strings.Contains(body, "org") {
		t.Errorf("body = %s, want the roles", body)
	}
}

// TestAdminGrantRole proves a system admin grants a role.
func TestAdminGrantRole(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPOST(t, ts, "/admin/users/x@hermex.test/roles", "", csrf, `{"role":"org","scopeID":4}`),
		http.StatusUnauthorized, "unauth grant role")
	wantEq(t, d.grantedRole, "", "unauth request granted role")

	wantStatus(t, authedPOST(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"org","scopeID":4}`),
		http.StatusNoContent, "grant role")
	if d.grantedRole != "org" || d.grantedScope != 4 {
		t.Errorf("granted %q scope %d, want org scope 4", d.grantedRole, d.grantedScope)
	}
}

// TestAdminRevokeRole proves a system admin revokes a role.
func TestAdminRevokeRole(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedDELETE(t, ts, "/admin/users/x@hermex.test/roles", "", csrf, `{"role":"domain","scopeID":9}`),
		http.StatusUnauthorized, "unauth revoke role")
	wantEq(t, d.revokedRole, "", "unauth request revoked role")

	wantStatus(t, authedDELETE(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"domain","scopeID":9}`),
		http.StatusNoContent, "revoke role")
	if d.revokedRole != "domain" || d.revokedScope != 9 {
		t.Errorf("revoked %q scope %d, want domain scope 9", d.revokedRole, d.revokedScope)
	}
}

// TestAdminRolesRequireSystem proves an org admin cannot manage roles, role
// administration is a system-level operation, so the org admin must not be able
// to escalate.
func TestAdminRolesRequireSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPOST(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"system"}`),
		http.StatusForbidden, "org-admin grant role")
	if d.grantedRole != "" {
		t.Errorf("an unauthorized request still granted %q", d.grantedRole)
	}
	// A revoke is refused too: stripping another admin's role is the same authority.
	wantStatus(t, authedDELETE(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"system"}`),
		http.StatusForbidden, "org-admin revoke role")
	if d.revokedRole != "" {
		t.Errorf("an unauthorized request still revoked %q", d.revokedRole)
	}
}
