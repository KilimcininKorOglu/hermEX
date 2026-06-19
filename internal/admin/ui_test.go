package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// noRedirectClient returns an HTTP client that surfaces redirects instead of
// following them, so a 303 can be inspected.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// TestUILoginPage proves the login form renders.
func TestUILoginPage(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	resp, err := http.Get(ts.URL + "/admin/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `name="login"`) || !strings.Contains(string(body), `name="password"`) {
		t.Errorf("login page missing the form fields: %s", body)
	}
}

// TestUILoginSubmit proves a valid login redirects to the dashboard with a
// session cookie.
func TestUILoginSubmit(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)

	form := url.Values{"login": {"admin@hermex.test"}, "password": {"pw"}}
	req, _ := http.NewRequest("POST", ts.URL+"/admin/ui/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login submit status %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/ui/" {
		t.Errorf("redirect to %q, want /admin/ui/", loc)
	}
	var hasSession bool
	for _, sc := range resp.Header["Set-Cookie"] {
		if strings.HasPrefix(sc, sessionCookie+"=") {
			hasSession = true
		}
	}
	if !hasSession {
		t.Error("login submit set no session cookie")
	}
}

// TestUILoginSubmitInvalid proves a bad login re-renders the form with an error.
func TestUILoginSubmitInvalid(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: false})
	form := url.Values{"login": {"x@hermex.test"}, "password": {"wrong"}}
	req, _ := http.NewRequest("POST", ts.URL+"/admin/ui/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid login status %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Invalid email or password") {
		t.Errorf("invalid login page missing the error: %s", body)
	}
}

// TestUIDashboard proves the dashboard renders the resource counts for a
// signed-in admin.
func TestUIDashboard(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		users:   []directory.UserInfo{{ID: 1}, {ID: 2}},
		domains: []directory.DomainInfo{{ID: 1}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	req, _ := http.NewRequest("GET", ts.URL+"/admin/ui/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "admin@hermex.test") || !strings.Contains(string(body), "Dashboard") {
		t.Errorf("dashboard missing the identity or title: %s", body)
	}
	if !strings.Contains(string(body), `<span class="num">2</span>`) ||
		!strings.Contains(string(body), `<span class="num">1</span>`) {
		t.Errorf("dashboard missing the user/domain counts: %s", body)
	}
}

// TestUIDashboardNoSession proves the dashboard redirects to login without a
// session.
func TestUIDashboardNoSession(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	req, _ := http.NewRequest("GET", ts.URL+"/admin/ui/", nil)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/ui/login" {
		t.Errorf("no-session dashboard = %d -> %q, want 303 -> /admin/ui/login",
			resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestUILogout proves logout needs a valid CSRF form token, then returns to the
// login page.
func TestUILogout(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	logout := func(field string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/admin/ui/logout",
			strings.NewReader(url.Values{"_csrf": {field}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
		resp, err := noRedirectClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	r1 := logout("")
	r1.Body.Close()
	if r1.StatusCode != http.StatusForbidden {
		t.Errorf("logout without the CSRF field = %d, want 403", r1.StatusCode)
	}

	r2 := logout(csrf)
	r2.Body.Close()
	if r2.StatusCode != http.StatusSeeOther || r2.Header.Get("Location") != "/admin/ui/login" {
		t.Errorf("logout with the CSRF field = %d -> %q, want 303 -> /admin/ui/login",
			r2.StatusCode, r2.Header.Get("Location"))
	}
}
