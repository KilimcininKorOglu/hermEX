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

// signInWithPrefs posts the sign-in form carrying the theme and language cookies
// the sign-in page's own controls set.
func signInWithPrefs(t *testing.T, ts *httptest.Server, theme, lang string) {
	t.Helper()
	form := url.Values{"login": {"admin@hermex.test"}, "password": {"pw"}}
	req := httptest.NewRequest(http.MethodPost, ts.URL+"/admin/ui/login", strings.NewReader(form.Encode()))
	req.RequestURI = ""
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: themeCookie, Value: theme})
	req.AddCookie(&http.Cookie{Name: langCookie, Value: lang})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in = %d, want 303", resp.StatusCode)
	}
}

// TestSignInAdoptsTheSignInPagePrefs proves a theme and language chosen on the
// sign-in page, before any session could store them, are written to the users
// record on sign-in where the record holds none. Without it the first full page
// load cleared the language cookie, so the choice was lost at sign-in.
func TestSignInAdoptsTheSignInPagePrefs(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		uiPrefs: map[string]directory.UserPrefs{"admin@hermex.test": {}},
	}
	signInWithPrefs(t, adminServer(t, d), "dark", "tr")
	if got := d.uiPrefs["admin@hermex.test"]; got.Theme != "dark" || got.Lang != "tr" {
		t.Errorf("stored prefs = %+v, want the dark theme and Turkish", got)
	}
}

// TestSignInPageOffersLanguageAndTheme proves the sign-in page carries the
// language selector and the theme switch, marked to cache the choice locally,
// and renders in the language its cookie names.
func TestSignInPageOffersLanguageAndTheme(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: true})
	req := httptest.NewRequest(http.MethodGet, ts.URL+"/admin/ui/login", nil)
	req.RequestURI = ""
	req.AddCookie(&http.Cookie{Name: langCookie, Value: "tr"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`data-prefs-local`, `class="lang-select"`, `class="ghost icon-button theme-toggle"`, `<html lang="tr"`, `<option value="tr" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-in page lacks %s", want)
		}
	}
}

// TestSignInKeepsTheStoredPrefs proves a choice the users record already holds
// wins over the sign-in page's cookies, and a cookie value that is no valid
// choice is never stored.
func TestSignInKeepsTheStoredPrefs(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		uiPrefs: map[string]directory.UserPrefs{"admin@hermex.test": {Theme: "light", Lang: "en"}},
	}
	signInWithPrefs(t, adminServer(t, d), "dark", "tr")
	if got := d.uiPrefs["admin@hermex.test"]; got.Theme != "light" || got.Lang != "en" {
		t.Errorf("stored prefs = %+v, want the stored light theme and English kept", got)
	}
	d.uiPrefs["admin@hermex.test"] = directory.UserPrefs{}
	signInWithPrefs(t, adminServer(t, d), "blue", "xx")
	if got := d.uiPrefs["admin@hermex.test"]; got.Theme != "" || got.Lang != "" {
		t.Errorf("stored prefs = %+v, want invalid cookie values ignored", got)
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
