package directory

import "testing"

// TestAdminSessionRoundTrip proves the admin session store: a created session is
// active until expiry, an absent jti is inactive, the login is matched
// case-insensitively, revocation is scoped to the owner so a known jti cannot sign
// another operator out, and a password change can clear every session an account
// holds at once.
func TestAdminSessionRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)

	const now = int64(1700000000)
	mk := func(jti, login string) {
		t.Helper()
		mustNoErr(t, "create panel session "+jti, d.CreateAdminSession(AdminSession{
			Jti: jti, Login: login, CreatedAt: now, ExpiresAt: now + 3600,
		}))
	}
	active := func(jti string, at int64) bool {
		t.Helper()
		a, err := d.AdminSessionActive(jti, at)
		mustNoErr(t, "read session state", err)
		return a
	}
	mk("adm-1", "Op@hermex.test")

	wantEq(t, "active before expiry", active("adm-1", now+1), true)
	wantEq(t, "active after expiry", active("adm-1", now+3601), false)
	wantEq(t, "an absent jti is active", active("nope", now+1), false)

	// Revoke is owner-scoped: another login must not delete it.
	mustNoErr(t, "revoke under a foreign login", d.DeleteAdminSession("other@hermex.test", "adm-1"))
	wantEq(t, "the session survived a foreign login's revoke", active("adm-1", now+1), true)
	// The owner revokes it, matched case-insensitively against the stored login.
	mustNoErr(t, "revoke under the owner login", d.DeleteAdminSession("op@hermex.test", "adm-1"))
	wantEq(t, "active after the owner's revoke", active("adm-1", now+1), false)

	// A password change clears every session the account holds, not just one.
	mk("adm-2", "op@hermex.test")
	mk("adm-3", "op@hermex.test")
	mk("adm-4", "other@hermex.test")
	mustNoErr(t, "revoke every session the account holds", d.DeleteAdminSessionsFor("OP@hermex.test"))
	for _, jti := range []string{"adm-2", "adm-3"} {
		wantEq(t, jti+" survived the full revoke", active(jti, now+1), false)
	}
	wantEq(t, "another account's session survived the full revoke", active("adm-4", now+1), true)
}

// TestEmergencySessionRevoke proves the compromise-response lever: one call ends
// every session an account holds on each signed-in surface, without the operator
// needing to know any identifier, and without touching anyone else. Until the
// session stores existed the only way to end a stolen cookie was to restart every
// daemon.
func TestEmergencySessionRevoke(t *testing.T) {
	d, _ := freshDirectory(t)

	const now = int64(1700000000)
	for _, jti := range []string{"web-1", "web-2"} {
		mustNoErr(t, "create webmail session", d.CreateWebmailSession(WebmailSession{
			Jti: jti, Email: "victim@hermex.test", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
		}))
	}
	mustNoErr(t, "create another account's session", d.CreateWebmailSession(WebmailSession{
		Jti: "web-other", Email: "other@hermex.test", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
	}))
	for _, jti := range []string{"adm-1", "adm-2"} {
		mustNoErr(t, "create panel session", d.CreateAdminSession(AdminSession{
			Jti: jti, Login: "victim@hermex.test", CreatedAt: now, ExpiresAt: now + 3600,
		}))
	}

	// An operator can see what is signed in before ending it.
	panel, err := d.ListAdminSessions("VICTIM@hermex.test", now)
	mustNoErr(t, "list panel sessions", err)
	wantEq(t, "listed panel sessions", len(panel), 2)

	web, err := d.DeleteWebmailSessionsFor("VICTIM@hermex.test")
	mustNoErr(t, "revoke webmail sessions", err)
	wantEq(t, "revoked webmail sessions", web, int64(2))
	adm, err := d.CountedDeleteAdminSessionsFor("victim@hermex.test")
	mustNoErr(t, "revoke panel sessions", err)
	wantEq(t, "revoked panel sessions", adm, int64(2))

	for _, jti := range []string{"web-1", "web-2"} {
		active, _ := d.WebmailSessionActive(jti, now+1)
		wantEq(t, "webmail session "+jti+" survived the revoke", active, false)
	}
	for _, jti := range []string{"adm-1", "adm-2"} {
		active, _ := d.AdminSessionActive(jti, now+1)
		wantEq(t, "panel session "+jti+" survived the revoke", active, false)
	}
	other, _ := d.WebmailSessionActive("web-other", now+1)
	wantEq(t, "another account's session survived (the revoke must not reach it)", other, true)
}
