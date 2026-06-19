package admin

import (
	"io"
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
	req, _ := http.NewRequest("DELETE", ts.URL+path, strings.NewReader(body))
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

	resp := authedGET(t, ts, "/admin/users/x@hermex.test/roles", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list roles status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "system") || !strings.Contains(string(body), "org") {
		t.Errorf("body = %s, want the roles", body)
	}
}

// TestAdminGrantRole proves a system admin grants a role.
func TestAdminGrantRole(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"org","scopeID":4}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("grant role status %d, want 204", resp.StatusCode)
	}
	if d.grantedRole != "org" || d.grantedScope != 4 {
		t.Errorf("granted %q scope %d, want org scope 4", d.grantedRole, d.grantedScope)
	}
}

// TestAdminRevokeRole proves a system admin revokes a role.
func TestAdminRevokeRole(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"domain","scopeID":9}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke role status %d, want 204", resp.StatusCode)
	}
	if d.revokedRole != "domain" || d.revokedScope != 9 {
		t.Errorf("revoked %q scope %d, want domain scope 9", d.revokedRole, d.revokedScope)
	}
}

// TestAdminRolesRequireSystem proves an org admin cannot manage roles — role
// administration is a system-level operation, so the org admin must not be able
// to escalate.
func TestAdminRolesRequireSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/x@hermex.test/roles", session, csrf, `{"role":"system"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin grant role = %d, want 403", resp.StatusCode)
	}
	if d.grantedRole != "" {
		t.Errorf("an unauthorized request still granted %q", d.grantedRole)
	}
}
