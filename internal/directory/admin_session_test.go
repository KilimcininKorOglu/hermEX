package directory

import "testing"

// TestAdminSessionRoundTrip proves the admin session store: a created session is
// active until expiry, an absent jti is inactive, the login is matched
// case-insensitively, revocation is scoped to the owner so a known jti cannot sign
// another operator out, and a password change can clear every session an account
// holds at once.
func TestAdminSessionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1700000000)
	mk := func(jti, login string) {
		t.Helper()
		if err := d.CreateAdminSession(AdminSession{
			Jti: jti, Login: login, CreatedAt: now, ExpiresAt: now + 3600,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("adm-1", "Op@hermex.test")

	if a, err := d.AdminSessionActive("adm-1", now+1); err != nil || !a {
		t.Fatalf("active before expiry = %v (err %v), want true", a, err)
	}
	if a, _ := d.AdminSessionActive("adm-1", now+3601); a {
		t.Error("session should be inactive after expiry")
	}
	if a, _ := d.AdminSessionActive("nope", now+1); a {
		t.Error("absent jti should be inactive")
	}

	// Revoke is owner-scoped: another login must not delete it.
	if err := d.DeleteAdminSession("other@hermex.test", "adm-1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := d.AdminSessionActive("adm-1", now+1); !a {
		t.Error("a foreign login revoked someone else's session")
	}
	// The owner revokes it, matched case-insensitively against the stored login.
	if err := d.DeleteAdminSession("op@hermex.test", "adm-1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := d.AdminSessionActive("adm-1", now+1); a {
		t.Error("the owner's revoke left the session active")
	}

	// A password change clears every session the account holds, not just one.
	mk("adm-2", "op@hermex.test")
	mk("adm-3", "op@hermex.test")
	mk("adm-4", "other@hermex.test")
	if err := d.DeleteAdminSessionsFor("OP@hermex.test"); err != nil {
		t.Fatal(err)
	}
	for _, jti := range []string{"adm-2", "adm-3"} {
		if a, _ := d.AdminSessionActive(jti, now+1); a {
			t.Errorf("%s survived a full revoke", jti)
		}
	}
	if a, _ := d.AdminSessionActive("adm-4", now+1); !a {
		t.Error("a full revoke reached another account's session")
	}
}
