package directory

import "testing"

// TestExpiredSessionsArePruned is the unbounded-growth defect. Both session tables
// document that rows are pruned by expiry, but every read only filtered on expiry
// and nothing ever deleted, so each login that ended by the browser closing rather
// than by an explicit logout left a row behind forever. A live session must survive
// the sweep, or the prune would log everyone out.
func TestExpiredSessionsArePruned(t *testing.T) {
	d, _ := freshDirectory(t)

	const now = int64(1700000000)
	for _, s := range []WebmailSession{
		{Jti: "jti-live", Email: "u@hermex.test", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600},
		{Jti: "jti-old", Email: "u@hermex.test", CreatedAt: now - 7200, LastActive: now - 7200, ExpiresAt: now - 60},
	} {
		mustNoErr(t, "create webmail session "+s.Jti, d.CreateWebmailSession(s))
	}
	for _, s := range []AdminSession{
		{Jti: "admin-old", Login: "root@hermex.test", CreatedAt: now - 7200, ExpiresAt: now - 60},
		{Jti: "admin-live", Login: "root@hermex.test", CreatedAt: now, ExpiresAt: now + 3600},
	} {
		mustNoErr(t, "create panel session "+s.Jti, d.CreateAdminSession(s))
	}

	n, err := d.PurgeExpiredWebmailSessions(now)
	mustNoErr(t, "prune webmail sessions", err)
	wantEq(t, "webmail sessions pruned (only the expired one)", n, int64(1))
	liveWeb, _ := d.WebmailSessionActive("jti-live", now)
	wantEq(t, "the live webmail session survived (the sweep must not log anyone out)", liveWeb, true)

	an, err := d.PurgeExpiredAdminSessions(now)
	mustNoErr(t, "prune panel sessions", err)
	wantEq(t, "panel sessions pruned (only the expired one)", an, int64(1))
	livePanel, _ := d.AdminSessionActive("admin-live", now)
	wantEq(t, "the live panel session survived", livePanel, true)

	// A second pass finds nothing left, so the sweep is idempotent and safe to run
	// from several instances.
	again, err := d.PurgeExpiredWebmailSessions(now)
	mustNoErr(t, "prune webmail sessions again", err)
	wantEq(t, "rows the second pass pruned", again, int64(0))
}
