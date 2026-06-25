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
