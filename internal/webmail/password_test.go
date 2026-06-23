package webmail

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// pwDir is a privilege-aware static directory that also records SetPassword
// calls, so a test can assert the new password reached the directory.
type pwDir struct {
	privDir
	setUser, setPass string
	calls            int
	ldap             bool // when true, accounts are LDAP/AD-backed
}

func (d *pwDir) SetPassword(user, newPassword string) (bool, error) {
	d.setUser, d.setPass = user, newPassword
	d.calls++
	return true, nil
}

// IsLDAPUser implements directory.LDAPIdentitySource for the test.
func (d *pwDir) IsLDAPUser(string) (bool, error) { return d.ldap, nil }

func newPwServer(t *testing.T, privs directory.ServicePrivileges) (*httptest.Server, *pwDir, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	auth := &pwDir{privDir: privDir{
		StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {Password: "secret", MailboxPath: dir}},
		privs:          privs,
	}}
	srv, err := NewServer(auth, auth, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	token := srv.sessions.create("alice@hermex.test", dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, auth, token
}

func postPassword(t *testing.T, ts *httptest.Server, token string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", ts.URL+"/password", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestWebmailChangePassword proves a privileged user can change their password:
// the current password is verified and the new one is written to the directory.
func TestWebmailChangePassword(t *testing.T) {
	ts, auth, token := newPwServer(t, directory.ServicePrivileges{ChgPasswd: true, Web: true})

	resp := postPassword(t, ts, token, url.Values{
		"current": {"secret"}, "new": {"newpassword1"}, "confirm": {"newpassword1"},
	})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "saved=1") {
		t.Fatalf("change redirected to %q, want saved=1", loc)
	}
	if auth.calls != 1 || auth.setPass != "newpassword1" || auth.setUser != "alice@hermex.test" {
		t.Errorf("SetPassword = (user %q, pass %q, calls %d), want the new password for alice once",
			auth.setUser, auth.setPass, auth.calls)
	}
}

// TestWebmailChangePasswordWrongCurrent proves the change is refused, and the new
// password never written, when the current password is wrong.
func TestWebmailChangePasswordWrongCurrent(t *testing.T) {
	ts, auth, token := newPwServer(t, directory.ServicePrivileges{ChgPasswd: true, Web: true})

	resp := postPassword(t, ts, token, url.Values{
		"current": {"wrong"}, "new": {"newpassword1"}, "confirm": {"newpassword1"},
	})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "err=current") {
		t.Fatalf("wrong-current redirected to %q, want err=current", loc)
	}
	if auth.calls != 0 {
		t.Errorf("SetPassword called %d times despite a wrong current password, want 0", auth.calls)
	}
}

// TestWebmailChangePasswordLDAPDenied proves an LDAP/AD-backed account cannot
// change its password locally even with the privilege: the submit is refused (the
// password lives in the external directory) and the directory is never written.
func TestWebmailChangePasswordLDAPDenied(t *testing.T) {
	ts, auth, token := newPwServer(t, directory.ServicePrivileges{ChgPasswd: true, Web: true})
	auth.ldap = true

	resp := postPassword(t, ts, token, url.Values{
		"current": {"secret"}, "new": {"newpassword1"}, "confirm": {"newpassword1"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("LDAP-account password change got %d, want 403", resp.StatusCode)
	}
	if auth.calls != 0 {
		t.Errorf("SetPassword called %d times for an LDAP account, want 0", auth.calls)
	}

	// The settings page must not offer the Password tab for an LDAP account.
	req, _ := http.NewRequest("GET", ts.URL+"/settings", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	r, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if strings.Contains(string(body), `data-tab="password"`) {
		t.Errorf("settings page offers the Password tab for an LDAP account")
	}
}

// TestWebmailChangePasswordDenied proves a user without the change-password
// privilege is refused with 403 and the directory is never written.
func TestWebmailChangePasswordDenied(t *testing.T) {
	ts, auth, token := newPwServer(t, directory.ServicePrivileges{ChgPasswd: false, Web: true})

	resp := postPassword(t, ts, token, url.Values{
		"current": {"secret"}, "new": {"newpassword1"}, "confirm": {"newpassword1"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("change without the privilege got %d, want 403", resp.StatusCode)
	}
	if auth.calls != 0 {
		t.Errorf("SetPassword called %d times despite no privilege, want 0", auth.calls)
	}
}
