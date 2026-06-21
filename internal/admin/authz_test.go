package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// authedReq issues an arbitrary-method request carrying the session and CSRF
// cookies plus the CSRF header, so a refusal is the authorization gate's, not the
// CSRF check's.
func authedReq(t *testing.T, ts *httptest.Server, method, path, session, csrf, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	req.Header.Set(csrfHeader, csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// statusOf returns a response's status and closes its body.
func statusOf(resp *http.Response) int {
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestDomainAdminScopedAccess proves a domain admin manages users in its own
// domain but is refused — both read and write — for another domain's users.
func TestDomainAdminScopedAccess(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		perms: []directory.Permission{{Name: directory.PermDomainAdmin, Params: "1"}},
		knownUsers: map[string]directory.UserDetail{
			"in@acme.test":   {Username: "in@acme.test", ID: 10, DomainID: 1},
			"out@other.test": {Username: "out@other.test", ID: 11, DomainID: 2},
		},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	if s := statusOf(authedGET(t, ts, "/admin/users/in@acme.test", session)); s == http.StatusForbidden {
		t.Errorf("domain admin denied read of own-domain user (403)")
	}
	if s := statusOf(authedReq(t, ts, "PUT", "/admin/users/in@acme.test", session, csrf, `{}`)); s == http.StatusForbidden {
		t.Errorf("domain admin denied write of own-domain user (403)")
	}
	if s := statusOf(authedGET(t, ts, "/admin/users/out@other.test", session)); s != http.StatusForbidden {
		t.Errorf("domain admin read of other-domain user = %d, want 403", s)
	}
	if s := statusOf(authedReq(t, ts, "PUT", "/admin/users/out@other.test", session, csrf, `{}`)); s != http.StatusForbidden {
		t.Errorf("domain admin write of other-domain user = %d, want 403", s)
	}
}

// TestDomainAdminROReadOnly proves a read-only domain admin reads its domain's
// users but cannot write them.
func TestDomainAdminROReadOnly(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		perms:      []directory.Permission{{Name: directory.PermDomainAdminRO, Params: "1"}},
		knownUsers: map[string]directory.UserDetail{"u@acme.test": {Username: "u@acme.test", ID: 10, DomainID: 1}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	if s := statusOf(authedGET(t, ts, "/admin/users/u@acme.test", session)); s == http.StatusForbidden {
		t.Errorf("read-only domain admin denied read of own-domain user (403)")
	}
	if s := statusOf(authedReq(t, ts, "PUT", "/admin/users/u@acme.test", session, csrf, `{}`)); s != http.StatusForbidden {
		t.Errorf("read-only domain admin write = %d, want 403", s)
	}
}

// TestDomainAdminCannotGrantRoles is the privilege-escalation boundary: a domain
// admin — even over all domains — cannot create roles or grant any tier to a
// user, so it can never escalate itself to system authority. Role administration
// stays full-system-admin-only.
func TestDomainAdminCannotGrantRoles(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		perms:      []directory.Permission{{Name: directory.PermDomainAdmin, Params: "*"}},
		knownUsers: map[string]directory.UserDetail{"u@acme.test": {Username: "u@acme.test", ID: 10, DomainID: 1}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	if s := statusOf(authedReq(t, ts, "POST", "/admin/roles", session, csrf, `{"name":"X"}`)); s != http.StatusForbidden {
		t.Errorf("domain admin create named role = %d, want 403", s)
	}
	if s := statusOf(authedReq(t, ts, "POST", "/admin/users/u@acme.test/roles", session, csrf, `{"role":"system"}`)); s != http.StatusForbidden {
		t.Errorf("domain admin grant tier = %d, want 403 (escalation boundary)", s)
	}
	if s := statusOf(authedGET(t, ts, "/admin/users/u@acme.test/roles", session)); s != http.StatusForbidden {
		t.Errorf("domain admin list user roles = %d, want 403", s)
	}
}

// TestReadOnlyAdminReadWriteSplit is the two-direction enforcement guarantee for
// a read-only system administrator: every read is admitted (never 403) and every
// state-changing request is refused (403). It pins the method-aware chokepoint —
// the single place that makes SystemAdminRO read-everything-write-nothing — and
// the two routes that deviate from it (the org LDAP scope and the password
// endpoint).
func TestReadOnlyAdminReadWriteSplit(t *testing.T) {
	d := &fakeDir{
		authOK: true,
		uid:    1,
		perms:  []directory.Permission{{Name: directory.PermSystemAdminRO}},
		orgs:   map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme"}},
		ldap:   map[int64]directory.LDAPConfig{1: {URI: "ldap://x"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// A read-only admin signs in (holds authority) — login itself must succeed.
	if session == "" {
		t.Fatal("read-only admin could not sign in")
	}

	// Reads: never blocked by authorization. The org-LDAP GET is the deviation
	// that a naive write-only scope check would wrongly refuse.
	reads := []string{
		"/admin/users",
		"/admin/domains",
		"/admin/aliases",
		"/admin/orgs",
		"/admin/orgs/1",
		"/admin/syncpolicy",
		"/admin/users/u@hermex.test/roles",
		"/admin/orgs/1/ldap",
	}
	for _, path := range reads {
		resp := authedGET(t, ts, path, session)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			t.Errorf("read %s: read-only admin got 403, want admitted", path)
		}
	}
	// Genuine admission (not merely a different error) on the unparameterized lists.
	for _, path := range []string{"/admin/users", "/admin/domains", "/admin/orgs", "/admin/syncpolicy"} {
		resp := authedGET(t, ts, path, session)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("read %s: status %d, want 200", path, resp.StatusCode)
		}
	}

	// Mutations: refused on every state change.
	muts := []struct{ method, path, body string }{
		{"POST", "/admin/users", `{"email":"x@hermex.test","password":"p"}`},
		{"PUT", "/admin/users/u@hermex.test", `{}`},
		{"DELETE", "/admin/users/u@hermex.test", `{}`},
		{"POST", "/admin/domains", `{"name":"x.test"}`},
		{"POST", "/admin/aliases", `{"aliasname":"a@hermex.test","mainname":"u@hermex.test"}`},
		{"POST", "/admin/orgs", `{"name":"X"}`},
		{"PUT", "/admin/orgs/1", `{"name":"Y"}`},
		{"DELETE", "/admin/orgs/1", ``},
		{"PUT", "/admin/orgs/1/domains/2", ``},
		{"PUT", "/admin/syncpolicy", `{}`},
		{"PUT", "/admin/orgs/1/ldap", `{"uri":"ldap://y"}`},
		{"POST", "/admin/users/u@hermex.test/roles", `{"role":"system"}`},
		{"DELETE", "/admin/users/u@hermex.test/roles", `{"role":"system"}`},
		{"POST", "/admin/users/u@hermex.test/password", `{"password":"newpw"}`},
	}
	for _, m := range muts {
		resp := authedReq(t, ts, m.method, m.path, session, csrf, m.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("mutation %s %s: read-only admin got %d, want 403", m.method, m.path, resp.StatusCode)
		}
	}
}

// TestFullAdminMayMutate is the positive control for the read/write split: a full
// system admin is not refused on a representative mutation, proving the gate
// admits writes rather than blocking everyone.
func TestFullAdminMayMutate(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 1, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := authedReq(t, ts, "POST", "/admin/domains", session, csrf, `{"name":"x.test"}`)
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("full system admin refused a mutation (403)")
	}
}

// TestResetPasswdCapabilityIsAdditive proves the password endpoint honors the
// ResetPasswd capability without it being a write bypass: a ResetPasswd holder
// may reset a password, a read-only admin without it may not, and the holder is
// still refused other mutations.
func TestResetPasswdCapabilityIsAdditive(t *testing.T) {
	// Read-only admin WITHOUT ResetPasswd: refused at the password endpoint.
	roOnly := &fakeDir{authOK: true, uid: 5, perms: []directory.Permission{{Name: directory.PermSystemAdminRO}}}
	ts := adminServer(t, roOnly)
	session, csrf := loginCookies(t, ts)
	resp := authedReq(t, ts, "POST", "/admin/users/u@hermex.test/password", session, csrf, `{"password":"newpw"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("RO without ResetPasswd: password reset got %d, want 403", resp.StatusCode)
	}

	// ResetPasswd holder: admitted at the password endpoint (204), refused elsewhere.
	helpdesk := &fakeDir{authOK: true, uid: 5, perms: []directory.Permission{{Name: directory.PermResetPasswd}}}
	ts2 := adminServer(t, helpdesk)
	session2, csrf2 := loginCookies(t, ts2)
	pwResp := authedReq(t, ts2, "POST", "/admin/users/u@hermex.test/password", session2, csrf2, `{"password":"newpw"}`)
	pwResp.Body.Close()
	if pwResp.StatusCode != http.StatusNoContent {
		t.Errorf("ResetPasswd holder: password reset got %d, want 204", pwResp.StatusCode)
	}
	otherResp := authedReq(t, ts2, "POST", "/admin/domains", session2, csrf2, `{"name":"x.test"}`)
	otherResp.Body.Close()
	if otherResp.StatusCode != http.StatusForbidden {
		t.Errorf("ResetPasswd holder: unrelated mutation got %d, want 403 (capability is not a general write grant)", otherResp.StatusCode)
	}
}
