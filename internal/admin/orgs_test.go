package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestAdminOrgCRUD proves a system admin can create, list, read, update, and
// delete an organization through the JSON API.
func TestAdminOrgCRUD(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/orgs", session, csrf, `{"name":"Acme","description":"The Acme org"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d, want 201", resp.StatusCode)
	}
	if len(d.orgs) != 1 {
		t.Fatalf("org not stored: %v", d.orgs)
	}
	var id int64
	for k := range d.orgs {
		id = k
	}

	list := authedGET(t, ts, "/admin/orgs", session)
	lbody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if !strings.Contains(string(lbody), "Acme") {
		t.Errorf("list missing the org: %s", lbody)
	}

	get := authedGET(t, ts, "/admin/orgs/"+itoa(id), session)
	gbody, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || !strings.Contains(string(gbody), "The Acme org") {
		t.Errorf("get = %d %s", get.StatusCode, gbody)
	}

	upd := authedPUT(t, ts, "/admin/orgs/"+itoa(id), session, csrf, `{"name":"Acme Inc","description":"renamed"}`)
	upd.Body.Close()
	if upd.StatusCode != http.StatusNoContent {
		t.Fatalf("update status %d, want 204", upd.StatusCode)
	}
	if d.orgs[id].Name != "Acme Inc" {
		t.Errorf("org not updated: %+v", d.orgs[id])
	}

	del := authedDELETE(t, ts, "/admin/orgs/"+itoa(id), session, csrf, "")
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d, want 204", del.StatusCode)
	}
	if len(d.orgs) != 0 {
		t.Errorf("org not deleted: %v", d.orgs)
	}

	missing := authedGET(t, ts, "/admin/orgs/"+itoa(id), session)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("get of a deleted org = %d, want 404", missing.StatusCode)
	}
}

// TestAdminDeleteOrgZeroRefused proves deleting the reserved organizationless id
// 0 is refused with a 400 (the directory rejects it), never silently accepted.
func TestAdminDeleteOrgZeroRefused(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/orgs/0", session, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("delete org 0 = %d, want 400", resp.StatusCode)
	}
}

// TestAdminAssignOrgDomain proves attaching and detaching a domain reaches the
// directory with the right ids and that an unknown domain is a 404.
func TestAdminAssignOrgDomain(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	att := authedPUT(t, ts, "/admin/orgs/5/domains/42", session, csrf, "")
	att.Body.Close()
	if att.StatusCode != http.StatusNoContent {
		t.Fatalf("attach status %d, want 204", att.StatusCode)
	}
	if d.assignDomainID != 42 || d.assignOrgID != 5 {
		t.Errorf("attach called with (%d,%d), want (42,5)", d.assignDomainID, d.assignOrgID)
	}

	det := authedDELETE(t, ts, "/admin/orgs/5/domains/42", session, csrf, "")
	det.Body.Close()
	if det.StatusCode != http.StatusNoContent {
		t.Fatalf("detach status %d, want 204", det.StatusCode)
	}
	if d.assignOrgID != 0 {
		t.Errorf("detach assigned org %d, want 0", d.assignOrgID)
	}

	d.assignDomainMissing = true
	miss := authedPUT(t, ts, "/admin/orgs/5/domains/999", session, csrf, "")
	miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Errorf("attach unknown domain = %d, want 404", miss.StatusCode)
	}
}

// TestAdminOrgRequiresSystem proves a domain admin cannot manage organizations.
func TestAdminOrgRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/orgs", session)
	get.Body.Close()
	post := authedPOST(t, ts, "/admin/orgs", session, csrf, `{"name":"X"}`)
	post.Body.Close()
	del := authedDELETE(t, ts, "/admin/orgs/1", session, csrf, "")
	del.Body.Close()
	if get.StatusCode != http.StatusForbidden || post.StatusCode != http.StatusForbidden || del.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin org access = GET %d / POST %d / DELETE %d, want all 403", get.StatusCode, post.StatusCode, del.StatusCode)
	}
}

// TestUIOrgsPage proves the organizations page lists a seeded organization.
func TestUIOrgsPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		orgs: map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme", Description: "desc", DomainCount: 2}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/orgs", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Acme") {
		t.Errorf("orgs page = %d, body lacks the org: %s", resp.StatusCode, body)
	}
}

// TestUIOrgCreate proves the create form stores an org and returns the refreshed
// panel showing it.
func TestUIOrgCreate(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/orgs", session, csrf, url.Values{"name": {"Acme"}, "description": {"d"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status %d, want 200", resp.StatusCode)
	}
	if len(d.orgs) != 1 {
		t.Fatalf("org not stored: %v", d.orgs)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Acme") {
		t.Errorf("refreshed panel missing the new org: %s", body)
	}
}

// TestUIOrgDetailAttach proves the detail page lists an unassigned domain and the
// add-domain form reaches the directory with the right ids.
func TestUIOrgDetailAttach(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		orgs:    map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme"}},
		domains: []directory.DomainInfo{{ID: 42, Name: "acme.test", OrgID: 0}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/ui/orgs/1", session)
	gbody, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || !strings.Contains(string(gbody), "acme.test") {
		t.Errorf("detail = %d, body lacks the available domain: %s", get.StatusCode, gbody)
	}

	att := htmxPOST(t, ts, "/admin/ui/orgs/1/domains", session, csrf, url.Values{"domainID": {"42"}})
	att.Body.Close()
	if att.StatusCode != http.StatusOK {
		t.Fatalf("attach status %d, want 200", att.StatusCode)
	}
	if d.assignDomainID != 42 || d.assignOrgID != 1 {
		t.Errorf("attach reached the directory with (%d,%d), want (42,1)", d.assignDomainID, d.assignOrgID)
	}
}

// TestUIOrgDelete proves the detail delete removes the org and redirects htmx
// back to the organizations page.
func TestUIOrgDelete(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		orgs: map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme"}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/orgs/1/delete", session, csrf, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("HX-Redirect") != "/admin/ui/orgs" {
		t.Errorf("delete = %d, HX-Redirect %q; want 200 + /admin/ui/orgs", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	if len(d.orgs) != 0 {
		t.Errorf("org not deleted: %v", d.orgs)
	}
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
