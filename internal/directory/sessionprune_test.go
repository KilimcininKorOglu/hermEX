package directory

import "testing"

// TestExpiredSessionsArePruned is the unbounded-growth defect. Both session tables
// document that rows are pruned by expiry, but every read only filtered on expiry
// and nothing ever deleted, so each login that ended by the browser closing rather
// than by an explicit logout left a row behind forever. A live session must survive
// the sweep, or the prune would log everyone out.
func TestExpiredSessionsArePruned(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1700000000)
	live := WebmailSession{
		Jti: "jti-live", Email: "u@hermex.test", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
	}
	expired := WebmailSession{
		Jti: "jti-old", Email: "u@hermex.test", CreatedAt: now - 7200, LastActive: now - 7200, ExpiresAt: now - 60,
	}
	for _, s := range []WebmailSession{live, expired} {
		if err := d.CreateWebmailSession(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.CreateAdminSession(AdminSession{
		Jti: "admin-old", Login: "root@hermex.test", CreatedAt: now - 7200, ExpiresAt: now - 60,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAdminSession(AdminSession{
		Jti: "admin-live", Login: "root@hermex.test", CreatedAt: now, ExpiresAt: now + 3600,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := d.PurgeExpiredWebmailSessions(now)
	if err != nil {
		t.Fatalf("prune webmail sessions: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d webmail sessions, want 1 (only the expired one)", n)
	}
	if a, _ := d.WebmailSessionActive("jti-live", now); !a {
		t.Error("the live webmail session was pruned; the sweep must not log anyone out")
	}

	an, err := d.PurgeExpiredAdminSessions(now)
	if err != nil {
		t.Fatalf("prune admin sessions: %v", err)
	}
	if an != 1 {
		t.Errorf("pruned %d admin sessions, want 1 (only the expired one)", an)
	}
	if a, _ := d.AdminSessionActive("admin-live", now); !a {
		t.Error("the live panel session was pruned")
	}

	// A second pass finds nothing left, so the sweep is idempotent and safe to run
	// from several instances.
	if again, _ := d.PurgeExpiredWebmailSessions(now); again != 0 {
		t.Errorf("second pass pruned %d rows, want 0", again)
	}
}
