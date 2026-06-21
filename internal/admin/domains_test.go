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
