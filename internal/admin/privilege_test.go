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
	wantStatus(t, resp, http.StatusOK, "edit privileges")
	got := d.updateUser
	if !got.POP3IMAP || got.SMTP || !got.Web || got.EAS || !got.DAV || got.ChgPasswd {
		t.Errorf("privilege payload = %+v, want POP3IMAP+Web+DAV on and SMTP/EAS/ChgPasswd off", got)
	}
}

// TestUIUserEditNeverReadsATypoAsNormal forges a status and a type that are not
// numbers. Read as 0 they reactivated a suspended account and turned a room into a
// mailbox; they must reach the directory as the refused -1.
func TestUIUserEditNeverReadsATypoAsNormal(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test", "", csrf,
		url.Values{"status": {"0"}}), http.StatusUnauthorized, "unauth user edit")
	wantEq(t, d.updateUser.Status, 0, "status before valid edit")

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test", session, csrf,
		url.Values{"status": {"active"}, "displayType": {"x"}, "homeserver": {"-2"}}),
		http.StatusOK, "forged user edit")
	got := d.updateUser
	if got.Status != -1 || got.DisplayType != -1 || got.Homeserver != -1 {
		t.Errorf("forged values reached the directory as status %d type %d homeserver %d, want -1 each",
			got.Status, got.DisplayType, got.Homeserver)
	}
}
