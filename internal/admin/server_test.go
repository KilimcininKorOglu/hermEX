package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// fakeDir is a scripted Directory for the admin server tests.
type fakeDir struct {
	authOK bool
	uid    int64
	roles  []directory.AdminRole
}

func (f *fakeDir) Authenticate(_, _ string) (string, bool) {
	if f.authOK {
		return "/mbox", true
	}
	return "", false
}
func (f *fakeDir) UserID(_ string) (int64, bool, error)            { return f.uid, f.uid != 0, nil }
func (f *fakeDir) AdminRoles(int64) ([]directory.AdminRole, error) { return f.roles, nil }

func adminServer(t *testing.T, d Directory) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewServer(d, []byte("test-secret")).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func login(t *testing.T, ts *httptest.Server) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"admin@hermex.test","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	return resp, resp.Header.Get("Set-Cookie")
}

// cookieValue extracts a named cookie's value from a Set-Cookie header (the
// Secure cookies would otherwise not ride back over httptest's plain HTTP).
func cookieValue(setCookie, name string) string {
	return strings.SplitN(strings.TrimPrefix(setCookie, name+"="), ";", 2)[0]
}

// loginCookies logs in and returns the session and CSRF cookie values.
func loginCookies(t *testing.T, ts *httptest.Server) (session, csrf string) {
	t.Helper()
	resp, _ := login(t, ts)
	resp.Body.Close()
	for _, sc := range resp.Header["Set-Cookie"] {
		if strings.HasPrefix(sc, sessionCookie+"=") {
			session = cookieValue(sc, sessionCookie)
		}
		if strings.HasPrefix(sc, csrfCookie+"=") {
			csrf = cookieValue(sc, csrfCookie)
		}
	}
	return session, csrf
}

// TestAdminLoginAndWhoami proves a valid admin login sets a session and whoami
// reports the admin's identity and roles.
func TestAdminLoginAndWhoami(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)

	resp, setCookie := login(t, ts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(setCookie, sessionCookie+"=") {
		t.Fatalf("login set no session cookie: %q", setCookie)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/admin/whoami", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookieValue(setCookie, sessionCookie)})
	who, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer who.Body.Close()
	if who.StatusCode != http.StatusOK {
		t.Fatalf("whoami status %d, want 200", who.StatusCode)
	}
	body, _ := io.ReadAll(who.Body)
	if !strings.Contains(string(body), "admin@hermex.test") || !strings.Contains(string(body), "system") {
		t.Errorf("whoami body = %s, want the login and the system role", body)
	}
}

// TestAdminLoginBadCreds proves wrong credentials are refused.
func TestAdminLoginBadCreds(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: false})
	resp, _ := login(t, ts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad-credentials status %d, want 401", resp.StatusCode)
	}
}

// TestAdminLoginNonAdmin proves a user who authenticates but holds no admin role
// is refused.
func TestAdminLoginNonAdmin(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: true, uid: 7, roles: nil})
	resp, _ := login(t, ts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin status %d, want 403", resp.StatusCode)
	}
}

// TestAdminCSRF proves a state-changing request needs a matching CSRF token: a
// logout with the session but no CSRF header is refused, and one carrying the
// header succeeds.
func TestAdminCSRF(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	if session == "" || csrf == "" {
		t.Fatalf("login set session=%q csrf=%q, want both", session, csrf)
	}

	withCookies := func(setHeader bool) int {
		req, _ := http.NewRequest("POST", ts.URL+"/admin/logout", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
		if setHeader {
			req.Header.Set(csrfHeader, csrf)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := withCookies(false); code != http.StatusForbidden {
		t.Errorf("logout without CSRF header = %d, want 403", code)
	}
	if code := withCookies(true); code != http.StatusNoContent {
		t.Errorf("logout with CSRF header = %d, want 204", code)
	}
}

// TestAdminWhoamiNoSession proves a protected endpoint refuses an unauthenticated
// request.
func TestAdminWhoamiNoSession(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	resp, err := http.Get(ts.URL + "/admin/whoami")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-session whoami status %d, want 401", resp.StatusCode)
	}
}
