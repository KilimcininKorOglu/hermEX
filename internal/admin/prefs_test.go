package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// themeCookieOf returns the theme cookie a response sets, or "" when it sets none.
func themeCookieOf(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == themeCookie {
			return c.Value
		}
	}
	return ""
}

// TestUISavePrefsStoresTheTheme proves the panel's theme switch stores the theme
// in the users record webmail shares and refreshes the cookie theme.js reads, for
// a panel user without system authority too, and refuses a bad value or a request
// without the CSRF header.
func TestUISavePrefsStoresTheTheme(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/prefs", session, csrf, url.Values{"theme": {"dark"}})
	resp.Body.Close()
	if _, langSet := langCookieOf(resp); resp.StatusCode != http.StatusNoContent || themeCookieOf(resp) != "dark" || langSet {
		t.Fatalf("save = %d, cookie %q, want 204 and the dark cookie", resp.StatusCode, themeCookieOf(resp))
	}
	if got := d.uiPrefs["admin@hermex.test"].Theme; got != "dark" {
		t.Errorf("stored theme = %q, want dark (prefs %v)", got, d.uiPrefs)
	}

	bad := htmxPUT(t, ts, "/admin/ui/prefs", session, csrf, url.Values{"theme": {"blue"}})
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad theme = %d, want 400", bad.StatusCode)
	}
	noCSRF := htmxPUT(t, ts, "/admin/ui/prefs", session, "", url.Values{"theme": {"light"}})
	noCSRF.Body.Close()
	if noCSRF.StatusCode != http.StatusForbidden || d.uiPrefs["admin@hermex.test"].Theme != "dark" {
		t.Errorf("a request without CSRF = %d, stored %q; want 403 and no change", noCSRF.StatusCode, d.uiPrefs["admin@hermex.test"].Theme)
	}
}

// TestPageLoadCarriesTheStoredTheme proves a full page load sets the theme cookie
// to the theme stored in the users record, so a theme chosen in webmail shows on
// the panel's first paint, and leaves a matching cookie and a panel fragment alone.
func TestPageLoadCarriesTheStoredTheme(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		uiPrefs: map[string]directory.UserPrefs{"admin@hermex.test": {Theme: "light"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	load := func(cookie string, htmx bool) *http.Response {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, ts.URL+"/admin/ui/", nil)
		req.RequestURI = ""
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: themeCookie, Value: cookie})
		}
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if got := themeCookieOf(load("dark", false)); got != "light" {
		t.Errorf("a page load with a stale cookie sets %q, want light", got)
	}
	if got := themeCookieOf(load("light", false)); got != "" {
		t.Errorf("a page load with the stored cookie sets %q, want none", got)
	}
	if got := themeCookieOf(load("dark", true)); got != "" {
		t.Errorf("a panel fragment sets %q, want none", got)
	}
}
