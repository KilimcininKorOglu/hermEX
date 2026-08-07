package directory

import "testing"

// TestWebmailSessionRoundTrip proves the webmail session store: a created session is
// active until expiry, an absent jti is inactive, listing lowercases the email and
// returns the row, and revocation is scoped to the owner email so a forged email
// cannot revoke another user's session (the IDOR guard the revoke endpoint relies on).
func TestWebmailSessionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1700000000)
	s := WebmailSession{
		Jti: "jti-1", Email: "U@hermex.test", DeviceType: "Chrome on macOS",
		UserAgent: "ua", ClientIP: "1.2.3.4", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
	}
	if err := d.CreateWebmailSession(s); err != nil {
		t.Fatal(err)
	}

	if a, err := d.WebmailSessionActive("jti-1", now+1); err != nil || !a {
		t.Fatalf("active before expiry = %v (err %v), want true", a, err)
	}
	if a, _ := d.WebmailSessionActive("jti-1", now+3601); a {
		t.Error("session should be inactive after expiry")
	}
	if a, _ := d.WebmailSessionActive("nope", now+1); a {
		t.Error("absent jti should be inactive")
	}

	// List lowercases the email (stored "U@..." found by "u@...").
	rows, err := d.ListWebmailSessions("u@hermex.test", now+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Jti != "jti-1" || rows[0].DeviceType != "Chrome on macOS" {
		t.Fatalf("list = %+v, want one jti-1 row", rows)
	}

	// Revoke is owner-scoped: a different email must NOT delete it.
	if ok, _ := d.DeleteWebmailSession("other@hermex.test", "jti-1"); ok {
		t.Error("revoke under a different email must not match (IDOR guard)")
	}
	if a, _ := d.WebmailSessionActive("jti-1", now+1); !a {
		t.Error("session must survive a cross-user revoke attempt")
	}
	// The owner revokes it; it is gone on the next check.
	if ok, _ := d.DeleteWebmailSession("u@hermex.test", "jti-1"); !ok {
		t.Error("revoke under the owner email should match")
	}
	if a, _ := d.WebmailSessionActive("jti-1", now+1); a {
		t.Error("session should be inactive after revoke")
	}
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
