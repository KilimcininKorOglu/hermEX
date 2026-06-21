package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// authedGET issues a GET carrying the session cookie and returns the response.
func authedGET(t *testing.T, ts *httptest.Server, path, session string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// authedPOST issues a POST carrying the session and CSRF cookies plus the CSRF
// header.
func authedPOST(t *testing.T, ts *httptest.Server, path, session, csrf, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
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

// TestAdminListDomains proves a system admin lists every domain.
func TestAdminListDomains(t *testing.T) {
	d := &fakeDir{
		authOK:  true,
		uid:     7,
		roles:   []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "hermex.test", OrgID: 0}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/domains", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list domains status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hermex.test") {
		t.Errorf("list body = %s, want the domain", body)
	}
}

// TestAdminListDomainsScopeFiltered proves the domain list is scope-filtered: a
// domain admin sees only its own domain, never another's. The filter must not
// leak out-of-scope rows.
func TestAdminListDomainsScopeFiltered(t *testing.T) {
	d := &fakeDir{
		authOK:  true,
		uid:     7,
		roles:   []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "other.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/domains", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("domain-admin list domains = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "acme.test") || strings.Contains(string(body), "other.test") {
		t.Errorf("domain admin saw out-of-scope domains: %s", body)
	}
}

// TestAdminGetDomain proves a system admin reads one domain's full record,
// including its user counts.
func TestAdminGetDomain(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test", MaxUser: 50, ActiveUsers: 3},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/domains/1", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get domain status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "acme.test") || !strings.Contains(string(body), "\"ActiveUsers\":3") {
		t.Errorf("get domain body = %s, want the detail with counts", body)
	}
}

// TestAdminGetDomainScoped proves the single-domain read is scope-gated: a domain
// admin reads its own domain but is refused another's.
func TestAdminGetDomainScoped(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		perms:        []directory.Permission{{Name: directory.PermDomainAdmin, Params: "1"}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	if s := statusOf(authedGET(t, ts, "/admin/domains/1", session)); s == http.StatusForbidden {
		t.Errorf("domain admin denied read of own domain (403)")
	}
	if s := statusOf(authedGET(t, ts, "/admin/domains/2", session)); s != http.StatusForbidden {
		t.Errorf("domain admin read of other domain = %d, want 403", s)
	}
}

// TestAdminUpdateDomain proves a system admin edits a domain and that the update
// is a read-merge: a field omitted from the request keeps its current value
// rather than being zeroed.
func TestAdminUpdateDomain(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test", Title: "Keep Me", Tel: "555"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := authedReq(t, ts, "PUT", "/admin/domains/1", session, csrf, `{"status":1,"maxUser":50}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update domain status %d, want 204", resp.StatusCode)
	}
	if d.updatedDomain != 1 || d.updateDomainArg.Status != 1 || d.updateDomainArg.MaxUser != 50 {
		t.Errorf("update arg = %+v, want status 1 / maxUser 50", d.updateDomainArg)
	}
	if d.updateDomainArg.Title != "Keep Me" || d.updateDomainArg.Tel != "555" {
		t.Errorf("read-merge zeroed an omitted field: %+v", d.updateDomainArg)
	}
}

// TestAdminUpdateDomainNotFound proves editing an unknown domain is a 404.
func TestAdminUpdateDomainNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		getDomainMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	if s := statusOf(authedReq(t, ts, "PUT", "/admin/domains/9", session, csrf, `{"status":1}`)); s != http.StatusNotFound {
		t.Errorf("update unknown domain = %d, want 404", s)
	}
}

// TestAdminUpdateDomainReadOnly proves domain edit requires full system authority:
// a read-only system admin is refused.
func TestAdminUpdateDomainReadOnly(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		perms:        []directory.Permission{{Name: directory.PermSystemAdminRO}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	if s := statusOf(authedReq(t, ts, "PUT", "/admin/domains/1", session, csrf, `{"status":1}`)); s != http.StatusForbidden {
		t.Errorf("read-only admin domain edit = %d, want 403", s)
	}
}

// TestAdminCreateDomain proves a system admin provisions a domain whose homedir
// is derived through the Paths deriver.
func TestAdminCreateDomain(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/domains", session, csrf, `{"name":"new.test"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create domain status %d, want 201", resp.StatusCode)
	}
	if d.createdDomain != "new.test" {
		t.Errorf("created domain %q, want new.test", d.createdDomain)
	}
	if !strings.HasSuffix(d.createdHomedir, "/dom/new.test") {
		t.Errorf("homedir %q not derived through Paths", d.createdHomedir)
	}
}

// TestAdminCreateDomainWithMaxUser proves the create endpoint applies a posted
// user limit to the new domain.
func TestAdminCreateDomainWithMaxUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := authedPOST(t, ts, "/admin/domains", session, csrf, `{"name":"new.test","maxUser":25}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create domain status %d, want 201", resp.StatusCode)
	}
	if d.createdDomain != "new.test" || d.updatedDomain != 42 || d.updateDomainArg.MaxUser != 25 {
		t.Errorf("create+limit = domain %q, updated id %d, maxUser %d; want new.test / 42 / 25",
			d.createdDomain, d.updatedDomain, d.updateDomainArg.MaxUser)
	}
}

// TestAdminCreateDomainNeedsCSRF proves the create route is CSRF-protected: a
// POST with the session but no CSRF header is refused (and provisions nothing).
func TestAdminCreateDomainNeedsCSRF(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	req, _ := http.NewRequest("POST", ts.URL+"/admin/domains", strings.NewReader(`{"name":"x.test"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("create without CSRF = %d, want 403", resp.StatusCode)
	}
	if d.createdDomain != "" {
		t.Errorf("a CSRF-less request still provisioned %q", d.createdDomain)
	}
}
