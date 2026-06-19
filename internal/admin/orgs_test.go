package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// authedPUT issues a PUT carrying the session and CSRF cookies plus the CSRF
// header.
func authedPUT(t *testing.T, ts *httptest.Server, path, session, csrf, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("PUT", ts.URL+path, strings.NewReader(body))
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

// TestAdminGetLDAP proves a system admin reads an org's LDAP config without the
// bind password leaking into the response.
func TestAdminGetLDAP(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		ldap: map[int64]directory.LDAPConfig{
			5: {URI: "ldaps://dc.hermex.test", BindDN: "cn=svc", BindPassword: "topsecret", BaseDN: "dc=hermex,dc=test"},
		},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/orgs/5/ldap", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get ldap status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ldaps://dc.hermex.test") {
		t.Errorf("body = %s, want the URI", body)
	}
	if strings.Contains(string(body), "topsecret") {
		t.Errorf("the bind password leaked in the response: %s", body)
	}
	if !strings.Contains(string(body), `"BindPasswordSet":true`) {
		t.Errorf("body = %s, want BindPasswordSet true", body)
	}
}

// TestAdminLDAPOrgScoped proves an org admin reads its own org's config but is
// refused another org's — the first real exercise of org-scoped RBAC.
func TestAdminLDAPOrgScoped(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 5}},
		ldap: map[int64]directory.LDAPConfig{5: {URI: "ldap://x"}, 9: {URI: "ldap://y"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	own := authedGET(t, ts, "/admin/orgs/5/ldap", session)
	own.Body.Close()
	if own.StatusCode != http.StatusOK {
		t.Errorf("own-org get = %d, want 200", own.StatusCode)
	}
	other := authedGET(t, ts, "/admin/orgs/9/ldap", session)
	other.Body.Close()
	if other.StatusCode != http.StatusForbidden {
		t.Errorf("other-org get = %d, want 403", other.StatusCode)
	}
}

// TestAdminPutLDAP proves a system admin writes a config, and a later write that
// omits the bind password keeps the stored one.
func TestAdminPutLDAP(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/orgs/5/ldap", session, csrf,
		`{"URI":"ldap://dc","BindDN":"cn=svc","BindPassword":"s3cret","BaseDN":"dc=x"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put ldap status %d, want 204", resp.StatusCode)
	}
	if got := d.ldap[5]; got.URI != "ldap://dc" || got.BindPassword != "s3cret" {
		t.Fatalf("stored config = %+v, want the posted values", got)
	}

	// A second write that omits the password keeps the stored one.
	resp2 := authedPUT(t, ts, "/admin/orgs/5/ldap", session, csrf, `{"URI":"ldap://dc2","BindDN":"cn=svc"}`)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("second put status %d, want 204", resp2.StatusCode)
	}
	if got := d.ldap[5]; got.URI != "ldap://dc2" || got.BindPassword != "s3cret" {
		t.Errorf("after a password-less put, config = %+v; want URI updated but password preserved", got)
	}
}
