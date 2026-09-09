package admin

import (
	"net/http"
	"testing"

	"hermex/internal/directory"
)

// enabledUser is an account with every protocol switched on, the state a partial
// update must not quietly undo.
func enabledUser() directory.UserDetail {
	return directory.UserDetail{
		ID: 4, Username: "alice@hermex.test", DomainID: 1, Status: 0,
		Lang: "tr", Timezone: "Europe/Istanbul", DisplayType: 0, Homeserver: 2,
		POP3IMAP: true, SMTP: true, ChgPasswd: true, Web: true, EAS: true, DAV: true,
	}
}

// TestUpdateUserLeavesOmittedFieldsAlone proves a partial update touches only
// what it names. Without the read-merge every protocol flag missing from the body
// decoded to false and was written as a deliberate revocation, so a caller that
// only meant to suspend an account also cut off its SMTP, IMAP, webmail,
// ActiveSync and DAV access.
func TestUpdateUserLeavesOmittedFieldsAlone(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: enabledUser(),
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/users/alice@hermex.test", session, csrf, `{"status":1}`)
	wantStatus(t, resp, http.StatusNoContent, "partial update")

	got := d.updateUser
	wantEq(t, got.Status, 1, "the requested status")
	wantTrue(t, got.POP3IMAP, "POP3/IMAP stays enabled")
	wantTrue(t, got.SMTP, "SMTP stays enabled")
	wantTrue(t, got.ChgPasswd, "password change stays enabled")
	wantTrue(t, got.Web, "webmail stays enabled")
	wantTrue(t, got.EAS, "ActiveSync stays enabled")
	wantTrue(t, got.DAV, "DAV stays enabled")
	wantEq(t, got.Lang, "tr", "the stored lang")
	wantEq(t, got.Timezone, "Europe/Istanbul", "the stored timezone")
	wantEq(t, got.Homeserver, 2, "the stored homeserver")
}

// TestUpdateUserStillRevokesWhenAsked is the negative control: an explicit false
// is a revocation and must go through, so the merge is not simply ignoring the
// flags.
func TestUpdateUserStillRevokesWhenAsked(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: enabledUser(),
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/users/alice@hermex.test", session, csrf,
		`{"smtp":false,"eas":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	got := d.updateUser
	if got.SMTP || got.EAS {
		t.Errorf("smtp=%v eas=%v, want both revoked as asked", got.SMTP, got.EAS)
	}
	if !got.POP3IMAP || !got.Web || !got.DAV || !got.ChgPasswd {
		t.Errorf("the unnamed flags changed too: %+v", got)
	}
}

// TestUpdateUserUnknownIsNotFound proves the read the merge needs also answers
// the missing-user case, rather than merging against a zero record.
func TestUpdateUserUnknownIsNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		getUserMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, "PUT", "/admin/users/ghost@hermex.test", session, csrf, `{"status":1}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if d.updatedUser != "" {
		t.Errorf("an unknown user was still written: %q", d.updatedUser)
	}
}
