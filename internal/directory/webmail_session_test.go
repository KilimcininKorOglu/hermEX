package directory

import "testing"

const sessionNow = int64(1700000000)

// mustAddSession stores one live session for email under jti.
func mustAddSession(t *testing.T, d *SQLDirectory, jti, email string) {
	t.Helper()
	mustNoErr(t, "create session "+jti, d.CreateWebmailSession(WebmailSession{
		Jti: jti, Email: email, CreatedAt: sessionNow, LastActive: sessionNow, ExpiresAt: sessionNow + 3600,
	}))
}

// sessionActive reports whether jti is live at the given second, requiring the read
// to succeed.
func sessionActive(t *testing.T, d *SQLDirectory, jti string, at int64) bool {
	t.Helper()
	a, err := d.WebmailSessionActive(jti, at)
	mustNoErr(t, "read session "+jti, err)
	return a
}

// TestWebmailSessionRoundTrip proves the webmail session store: a created session is
// active until expiry, an absent jti is inactive, listing lowercases the email and
// returns the row, and revocation is scoped to the owner email so a forged email
// cannot revoke another user's session (the IDOR guard the revoke endpoint relies on).
func TestWebmailSessionRoundTrip(t *testing.T) {
	d := freshSQL(t)
	mustNoErr(t, "create session", d.CreateWebmailSession(WebmailSession{
		Jti: "jti-1", Email: "U@hermex.test", DeviceType: "Chrome on macOS",
		UserAgent: "ua", ClientIP: "1.2.3.4", CreatedAt: sessionNow, LastActive: sessionNow, ExpiresAt: sessionNow + 3600,
	}))

	wantEq(t, "active before expiry", sessionActive(t, d, "jti-1", sessionNow+1), true)
	wantEq(t, "active after expiry", sessionActive(t, d, "jti-1", sessionNow+3601), false)
	wantEq(t, "an absent jti is active", sessionActive(t, d, "nope", sessionNow+1), false)

	// List lowercases the email (stored "U@..." found by "u@...").
	rows, err := d.ListWebmailSessions("u@hermex.test", sessionNow+1)
	mustNoErr(t, "list sessions", err)
	if len(rows) != 1 {
		t.Fatalf("list = %+v, want one jti-1 row", rows)
	}
	wantEq(t, "listed jti", rows[0].Jti, "jti-1")
	wantEq(t, "listed device", rows[0].DeviceType, "Chrome on macOS")
	// Listing is owner-scoped too: another account sees none of u's devices.
	foreign, err := d.ListWebmailSessions("other@hermex.test", sessionNow+1)
	mustNoErr(t, "list another account's sessions", err)
	wantEq(t, "sessions listed for another account", len(foreign), 0)

	// Revoke is owner-scoped: a different email must NOT delete it.
	crossUser, err := d.DeleteWebmailSession("other@hermex.test", "jti-1")
	mustNoErr(t, "revoke under another email", err)
	wantEq(t, "a revoke under a different email matched (IDOR guard)", crossUser, false)
	wantEq(t, "the session survived a cross-user revoke", sessionActive(t, d, "jti-1", sessionNow+1), true)
	// The owner revokes it; it is gone on the next check.
	owner, err := d.DeleteWebmailSession("u@hermex.test", "jti-1")
	mustNoErr(t, "revoke under the owner email", err)
	wantEq(t, "a revoke under the owner email matched", owner, true)
	wantEq(t, "active after the revoke", sessionActive(t, d, "jti-1", sessionNow+1), false)
}

// TestDeleteOtherWebmailSessions proves the query a password change runs: every
// session the account holds goes except the one named, and no other account is
// touched. Scoping by email as well as jti is what stops one user's password change
// from ending another user's sessions.
func TestDeleteOtherWebmailSessions(t *testing.T) {
	d := freshSQL(t)
	mustAddSession(t, d, "mine", "alice@hermex.test")
	mustAddSession(t, d, "stolen", "alice@hermex.test")
	mustAddSession(t, d, "old-phone", "alice@hermex.test")
	mustAddSession(t, d, "bob-1", "bob@hermex.test")

	// Uppercase on the way in: a login's case must not decide whether the
	// remediation finds the sessions to revoke.
	n, err := d.DeleteOtherWebmailSessions("ALICE@hermex.test", "mine")
	mustNoErr(t, "revoke the other sessions", err)
	wantEq(t, "sessions revoked", n, int64(2))
	wantEq(t, "the caller's own session", sessionActive(t, d, "mine", sessionNow+1), true)
	for _, jti := range []string{"stolen", "old-phone"} {
		wantEq(t, "session "+jti+" after the revocation", sessionActive(t, d, jti, sessionNow+1), false)
	}
	wantEq(t, "another account's session", sessionActive(t, d, "bob-1", sessionNow+1), true)
}

// TestDeleteOtherWebmailSessionsWithNoCurrentSession covers a caller whose token
// carries no jti, from an install that records no sessions or a token minted before
// they were. Keeping nothing is the right answer there: the caller's own session is
// not in the table, so every row that IS in it belongs to some other browser.
func TestDeleteOtherWebmailSessionsWithNoCurrentSession(t *testing.T) {
	d := freshSQL(t)
	mustAddSession(t, d, "a", "alice@hermex.test")
	mustAddSession(t, d, "b", "alice@hermex.test")
	n, err := d.DeleteOtherWebmailSessions("alice@hermex.test", "")
	mustNoErr(t, "revoke every session", err)
	wantEq(t, "sessions revoked", n, int64(2))
}
