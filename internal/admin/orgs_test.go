package admin

import (
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
	req := newRequest(t, "PUT", ts.URL+path, strings.NewReader(body))
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

	unauth := authedGET(t, ts, "/admin/orgs", "invalid-session")
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated orgs GET")

	create := authedPOST(t, ts, "/admin/orgs", session, csrf, `{"name":"Acme","description":"The Acme org"}`)
	wantStatus(t, create, http.StatusCreated, "create org")
	if len(d.orgs) != 1 {
		t.Fatalf("org not stored: %v", d.orgs)
	}
	var id int64
	for k := range d.orgs {
		id = k
	}

	list := wantBody(t, authedGET(t, ts, "/admin/orgs", session), http.StatusOK, "list orgs")
	wantContains(t, list, "Acme", "the listing carries the created org")
	get := wantBody(t, authedGET(t, ts, "/admin/orgs/"+itoa(id), session), http.StatusOK, "get org")
	wantContains(t, get, "The Acme org", "the read carries the stored description")

	upd := authedPUT(t, ts, "/admin/orgs/"+itoa(id), session, csrf, `{"name":"Acme Inc","description":"renamed"}`)
	wantStatus(t, upd, http.StatusNoContent, "update org")
	wantEq(t, d.orgs[id].Name, "Acme Inc", "the stored name after the update")

	del := authedDELETE(t, ts, "/admin/orgs/"+itoa(id), session, csrf, "")
	wantStatus(t, del, http.StatusNoContent, "delete org")
	wantEq(t, len(d.orgs), 0, "stored orgs after the delete")

	missing := authedGET(t, ts, "/admin/orgs/"+itoa(id), session)
	wantStatus(t, missing, http.StatusNotFound, "get of a deleted org")
}

// TestAdminDeleteOrgZeroRefused proves deleting the reserved organizationless id
// 0 is refused with a 400 (the directory rejects it), never silently accepted.
func TestAdminDeleteOrgZeroRefused(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/orgs/0", session, csrf, "")
	wantStatus(t, resp, http.StatusBadRequest, "delete org 0")
}

// TestAdminAssignOrgDomain proves attaching and detaching a domain reaches the
// directory with the right ids and that an unknown domain is a 404.
func TestAdminAssignOrgDomain(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	att := authedPUT(t, ts, "/admin/orgs/5/domains/42", session, csrf, "")
	wantStatus(t, att, http.StatusNoContent, "attach status")
	if d.assignDomainID != 42 || d.assignOrgID != 5 {
		t.Errorf("attach called with (%d,%d), want (42,5)", d.assignDomainID, d.assignOrgID)
	}

	det := authedDELETE(t, ts, "/admin/orgs/5/domains/42", session, csrf, "")
	wantStatus(t, det, http.StatusNoContent, "detach status")
	if d.assignOrgID != 0 {
		t.Errorf("detach assigned org %d, want 0", d.assignOrgID)
	}

	d.assignDomainMissing = true
	miss := authedPUT(t, ts, "/admin/orgs/5/domains/999", session, csrf, "")
	wantStatus(t, miss, http.StatusNotFound, "attach unknown domain")
}

// TestAdminOrgRequiresSystem proves a domain admin cannot manage organizations.
func TestAdminOrgRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		orgs: map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme"}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// Attaching a domain to an organization hands the organization's admins that
	// domain, so the attach is refused on both surfaces, and so is every org write.
	wantStatus(t, authedPUT(t, ts, "/admin/orgs/1/domains/1", session, csrf, ""), http.StatusForbidden, "attach")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/orgs/1/domains", session, csrf, url.Values{"domainID": {"1"}}),
		http.StatusForbidden, "UI attach")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/orgs", session, csrf, url.Values{"name": {"Evil"}}), http.StatusForbidden, "UI create")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/orgs/1/delete", session, csrf, url.Values{}), http.StatusForbidden, "UI delete")
	wantEq(t, d.assignDomainID, int64(0), "the domain a refused attach reached")
	if len(d.orgs) != 1 || d.orgs[1].Name != "Acme" {
		t.Errorf("a domain admin changed the organizations: %+v", d.orgs)
	}

	get := authedGET(t, ts, "/admin/orgs", session)
	wantStatus(t, get, http.StatusForbidden, "domain admin orgs get")
	post := authedPOST(t, ts, "/admin/orgs", session, csrf, `{"name":"X"}`)
	wantStatus(t, post, http.StatusForbidden, "domain admin orgs post")
	del := authedDELETE(t, ts, "/admin/orgs/1", session, csrf, "")
	wantStatus(t, del, http.StatusForbidden, "domain admin orgs delete")
}

// TestUIOrgsPage proves the organizations page lists a seeded organization.
func TestUIOrgsPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		orgs: map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme", Description: "desc", DomainCount: 2}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	body := wantBody(t, authedGET(t, ts, "/admin/ui/orgs", session), http.StatusOK, "orgs page")
	if !strings.Contains(body, "Acme") {
		t.Errorf("orgs page lacking the org: %s", body)
	}
}

// TestUIOrgCreate proves the create form stores an org and returns the refreshed
// panel showing it.
func TestUIOrgCreate(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/orgs", session, csrf, url.Values{"name": {"Acme"}, "description": {"d"}})
	body := wantBody(t, resp, http.StatusOK, "create org")
	if len(d.orgs) != 1 {
		t.Fatalf("org not stored: %v", d.orgs)
	}
	if !strings.Contains(body, "Acme") {
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

	gbody := wantBody(t, authedGET(t, ts, "/admin/ui/orgs/1", session), http.StatusOK, "detail get")
	if !strings.Contains(gbody, "acme.test") {
		t.Errorf("detail lacking the available domain: %s", gbody)
	}

	att := htmxPOST(t, ts, "/admin/ui/orgs/1/domains", session, csrf, url.Values{"domainID": {"42"}})
	wantStatus(t, att, http.StatusOK, "attach status")
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
	wantStatus(t, resp, http.StatusOK, "delete org")
	if resp.Header.Get("HX-Redirect") != "/admin/ui/orgs" {
		t.Errorf("delete HX-Redirect %q; want /admin/ui/orgs", resp.Header.Get("HX-Redirect"))
	}
	if len(d.orgs) != 0 {
		t.Errorf("org not deleted: %v", d.orgs)
	}
}
