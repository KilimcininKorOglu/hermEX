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

// htmxPOST issues an htmx-style POST with the session and CSRF cookies plus the
// X-CSRF-Token header (the double-submit htmx sends).
func htmxPOST(t *testing.T, ts *httptest.Server, path, session, csrf string, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(csrfHeader, csrf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestUIUsersPage proves the users page lists users for a system admin.
func TestUIUsersPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		users: []directory.UserInfo{{ID: 1, Username: "boss@hermex.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("users page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "boss@hermex.test") || !strings.Contains(string(body), "Add user") {
		t.Errorf("users page missing the user or the create form: %s", body)
	}
}

// TestUIUsersPageRequiresSystem proves an org admin cannot view the users page.
func TestUIUsersPageRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin users page = %d, want 403", resp.StatusCode)
	}
}

// TestUICreateUser proves the management form creates a user and returns the
// refreshed panel fragment.
func TestUICreateUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users", session, csrf,
		url.Values{"email": {"new@hermex.test"}, "password": {"pw"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create user status %d, want 200", resp.StatusCode)
	}
	if d.createdUser != "new@hermex.test" {
		t.Errorf("created user %q, want new@hermex.test", d.createdUser)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="users-panel"`) {
		t.Errorf("response is not the users panel fragment: %s", body)
	}
}

// TestUICreateUserNoCSRF proves the create form requires a CSRF token.
func TestUICreateUserNoCSRF(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	req, _ := http.NewRequest("POST", ts.URL+"/admin/ui/users",
		strings.NewReader("email=x@hermex.test&password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("create without CSRF = %d, want 403", resp.StatusCode)
	}
	if d.createdUser != "" {
		t.Errorf("a CSRF-less create still provisioned %q", d.createdUser)
	}
}

// TestUICreateUserValidation proves a missing field is reported in the panel
// without provisioning anything.
func TestUICreateUserValidation(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users", session, csrf, url.Values{"email": {"x@hermex.test"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validation response status %d, want 200 (panel with error)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "required") {
		t.Errorf("validation panel missing the error: %s", body)
	}
	if d.createdUser != "" {
		t.Errorf("an invalid create still provisioned %q", d.createdUser)
	}
}
