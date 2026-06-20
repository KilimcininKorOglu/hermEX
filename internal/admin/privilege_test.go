package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// TestUIUserEditPrivileges proves the account form's service-privilege checkboxes
// (web/EAS/DAV/password-change) carry through to the directory update per service,
// so an admin toggling one actually changes what that protocol enforces.
func TestUIUserEditPrivileges(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test", session, csrf, url.Values{
		"status":    {"0"},
		"pop3_imap": {"on"},
		"web":       {"on"},
		"dav":       {"on"},
		// smtp, eas, chgpasswd intentionally left unchecked
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit status %d, want 200", resp.StatusCode)
	}
	got := d.updateUser
	if !got.POP3IMAP || got.SMTP || !got.Web || got.EAS || !got.DAV || got.ChgPasswd {
		t.Errorf("privilege payload = %+v, want POP3IMAP+Web+DAV on and SMTP/EAS/ChgPasswd off", got)
	}
}
