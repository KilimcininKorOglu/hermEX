package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// cpFakeDir is a system admin whose stored password is "pw" (the one login() uses),
// so Authenticate accepts only "pw" and the wrong-current-password path is real.
func cpFakeDir() *fakeDir {
	return &fakeDir{authOK: true, password: "pw", uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
}

// TestUIChangePasswordPage proves the self-service page renders with the form
// pointed at the change-password endpoint and the caller's own login shown.
func TestUIChangePasswordPage(t *testing.T) {
	ts := adminServer(t, cpFakeDir())
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/change-password", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change-password page status %d, want 200", resp.StatusCode)
	}
	s := string(body)
	if !strings.Contains(s, `hx-put="/admin/ui/change-password"`) || !strings.Contains(s, "admin@hermex.test") {
		t.Errorf("change-password page missing the form or the login: %s", s)
	}
}

// TestUIChangePasswordSubmit proves a correct current password changes the
// caller's own account (taken from the session, not the form) and swaps a
// success message.
func TestUIChangePasswordSubmit(t *testing.T) {
	d := cpFakeDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/change-password", session, csrf,
		url.Values{"old": {"pw"}, "new": {"newsecret"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status %d, want 200", resp.StatusCode)
	}
	if d.setPwUser != "admin@hermex.test" || d.setPwValue != "newsecret" {
		t.Errorf("changed password for %q = %q, want admin@hermex.test = newsecret", d.setPwUser, d.setPwValue)
	}
	if !strings.Contains(string(body), "changed") {
		t.Errorf("success result missing: %s", body)
	}
}

// TestUIChangePasswordWrongCurrent proves a wrong current password reports an
// error and never touches the stored password.
func TestUIChangePasswordWrongCurrent(t *testing.T) {
	d := cpFakeDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/change-password", session, csrf,
		url.Values{"old": {"wrong"}, "new": {"newsecret"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit status %d, want 200", resp.StatusCode)
	}
	if d.setPwValue != "" {
		t.Errorf("password changed to %q despite a wrong current password", d.setPwValue)
	}
	if !strings.Contains(string(body), "incorrect") {
		t.Errorf("error result missing: %s", body)
	}
}
