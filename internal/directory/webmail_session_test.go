package directory

import "testing"

// TestWebmailSessionRoundTrip proves the webmail session store: a created session is
// active until expiry, an absent jti is inactive, listing lowercases the email and
// returns the row, and revocation is scoped to the owner email so a forged email
// cannot revoke another user's session (the IDOR guard the revoke endpoint relies on).
func TestWebmailSessionRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)

	const now = int64(1700000000)
	mustNoErr(t, "create session", d.CreateWebmailSession(WebmailSession{
		Jti: "jti-1", Email: "U@hermex.test", DeviceType: "Chrome on macOS",
		UserAgent: "ua", ClientIP: "1.2.3.4", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
	}))
	active := func(jti string, at int64) bool {
		t.Helper()
		a, err := d.WebmailSessionActive(jti, at)
		mustNoErr(t, "read session state", err)
		return a
	}

	wantEq(t, "active before expiry", active("jti-1", now+1), true)
	wantEq(t, "active after expiry", active("jti-1", now+3601), false)
	wantEq(t, "an absent jti is active", active("nope", now+1), false)

	// List lowercases the email (stored "U@..." found by "u@...").
	rows, err := d.ListWebmailSessions("u@hermex.test", now+1)
	mustNoErr(t, "list sessions", err)
	if len(rows) != 1 {
		t.Fatalf("list = %+v, want one jti-1 row", rows)
	}
	wantEq(t, "listed jti", rows[0].Jti, "jti-1")
	wantEq(t, "listed device", rows[0].DeviceType, "Chrome on macOS")

	// Revoke is owner-scoped: a different email must NOT delete it.
	crossUser, _ := d.DeleteWebmailSession("other@hermex.test", "jti-1")
	wantEq(t, "a revoke under a different email matched (IDOR guard)", crossUser, false)
	wantEq(t, "the session survived a cross-user revoke", active("jti-1", now+1), true)
	// The owner revokes it; it is gone on the next check.
	owner, _ := d.DeleteWebmailSession("u@hermex.test", "jti-1")
	wantEq(t, "a revoke under the owner email matched", owner, true)
	wantEq(t, "active after the revoke", active("jti-1", now+1), false)
}

// TestDeleteOtherWebmailSessions proves the query a password change runs: every
// session the account holds goes except the one named, and no other account is
// touched. Scoping by email as well as jti is what stops one user's password change
// from ending another user's sessions.
func TestDeleteOtherWebmailSessions(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1700000000)
	add := func(jti, email string) {
		t.Helper()
		if err := d.CreateWebmailSession(WebmailSession{
			Jti: jti, Email: email, CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("mine", "alice@hermex.test")
	add("stolen", "alice@hermex.test")
	add("old-phone", "alice@hermex.test")
	add("bob-1", "bob@hermex.test")

	// Uppercase on the way in: a login's case must not decide whether the
	// remediation finds the sessions to revoke.
	n, err := d.DeleteOtherWebmailSessions("ALICE@hermex.test", "mine")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("revoked %d sessions, want 2", n)
	}
	if a, _ := d.WebmailSessionActive("mine", now+1); !a {
		t.Error("the caller's own session was revoked")
	}
	for _, jti := range []string{"stolen", "old-phone"} {
		if a, _ := d.WebmailSessionActive(jti, now+1); a {
			t.Errorf("session %q survived the revocation", jti)
		}
	}
	if a, _ := d.WebmailSessionActive("bob-1", now+1); !a {
		t.Error("another account's session was revoked")
	}
}

// TestDeleteOtherWebmailSessionsWithNoCurrentSession covers a caller whose token
// carries no jti, from an install that records no sessions or a token minted before
// they were. Keeping nothing is the right answer there: the caller's own session is
// not in the table, so every row that IS in it belongs to some other browser.
func TestDeleteOtherWebmailSessionsWithNoCurrentSession(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1700000000)
	for _, jti := range []string{"a", "b"} {
		if err := d.CreateWebmailSession(WebmailSession{
			Jti: jti, Email: "alice@hermex.test", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := d.DeleteOtherWebmailSessions("alice@hermex.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("revoked %d sessions, want every one of the 2", n)
	}
}
