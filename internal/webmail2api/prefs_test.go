package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// prefsAuth is a directory stub that authenticates every account and keeps one
// user's interface preferences in memory, with the directory's validation.
type prefsAuth struct {
	mbox  string
	prefs *directory.UserPrefs
}

func (a prefsAuth) Authenticate(string, string) (string, bool) { return a.mbox, true }

func (a prefsAuth) GetUser(string) (directory.UserDetail, bool, error) {
	return directory.UserDetail{Lang: a.prefs.Lang}, true, nil
}

func (a prefsAuth) GetUserPrefs(string) (directory.UserPrefs, bool, error) {
	return *a.prefs, true, nil
}

func (a prefsAuth) SetUserPrefs(_ string, u directory.UserPrefsUpdate) (bool, error) {
	if u.Lang != nil && *u.Lang != "" && !slices.Contains(directory.UILanguages, *u.Lang) {
		return false, directory.ErrInvalidPref
	}
	if u.Theme != nil {
		a.prefs.Theme = *u.Theme
	}
	if u.Lang != nil {
		a.prefs.Lang = *u.Lang
	}
	if u.ShowWelcome != nil {
		a.prefs.ShowWelcome = *u.ShowWelcome
	}
	return true, nil
}

// TestPrefsRoundTrip proves a preference PUT is stored in the directory record
// and read back by /auth/me, the probe every page load makes, so a choice made on
// one browser (or in the admin panel) follows the user to the next.
func TestPrefsRoundTrip(t *testing.T) {
	stored := &directory.UserPrefs{ShowWelcome: true}
	call := prefsCaller(t, stored)
	const prefs = "/api/v1/account/prefs"

	code, out := call(http.MethodPut, prefs, `{"theme":"dark","locale":"tr","show_welcome_banner":false}`)
	wantPrefs(t, "PUT", code, out, "dark", "tr", false)
	code, me := call(http.MethodGet, "/api/v1/auth/me", "")
	wantPrefs(t, "/auth/me", code, me, "dark", "tr", false)
	// A partial PUT keeps the fields it does not name.
	code, out = call(http.MethodPut, prefs, `{"theme":"light"}`)
	wantPrefs(t, "partial PUT", code, out, "light", "tr", false)

	for _, body := range []string{`{"locale":"de"}`, `{}`, `not json`} {
		if code, _ := call(http.MethodPut, prefs, body); code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, code)
		}
	}
	if stored.Lang != "tr" {
		t.Errorf("a refused PUT changed the language to %q", stored.Lang)
	}
}

// prefsCaller returns a signed-in caller of a server over prefsAuth holding stored.
func prefsCaller(t *testing.T, stored *directory.UserPrefs) func(method, target, body string) (int, map[string]any) {
	t.Helper()
	secret := []byte("prefs-test-secret")
	srv := NewServer(prefsAuth{mbox: t.TempDir(), prefs: stored}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return func(method, target, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out) // a non-JSON body leaves out nil, which wantPrefs reports
		return rec.Code, out
	}
}

// wantPrefs checks an answer carries the given preferences.
func wantPrefs(t *testing.T, name string, code int, got map[string]any, theme, locale string, banner bool) {
	t.Helper()
	if code != http.StatusOK || got["theme"] != theme || got["locale"] != locale || got["show_welcome_banner"] != banner {
		t.Errorf("%s = %d %v, want theme %q, locale %q, banner %v", name, code, got, theme, locale, banner)
	}
}
