package directory

import "testing"

// TestExpiredSessionsArePruned is the unbounded-growth defect. Both session tables
// document that rows are pruned by expiry, but every read only filtered on expiry
// and nothing ever deleted, so each login that ended by the browser closing rather
// than by an explicit logout left a row behind forever. A live session must survive
// the sweep, or the prune would log everyone out.
func TestExpiredSessionsArePruned(t *testing.T) {
	d := freshSQL(t)

	const now = int64(1700000000)
	for _, s := range []WebmailSession{
		{Jti: "jti-live", Email: "u@hermex.test", CreatedAt: now, LastActive: now, ExpiresAt: now + 3600},
		{Jti: "jti-old", Email: "u@hermex.test", CreatedAt: now - 7200, LastActive: now - 7200, ExpiresAt: now - 60},
		// Expiring exactly now: the active check already refuses it (expires_at > now),
		// so the prune must remove it too.
		{Jti: "jti-edge", Email: "u@hermex.test", CreatedAt: now - 3600, LastActive: now - 3600, ExpiresAt: now},
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
	wantEq(t, "webmail sessions pruned (the two expired ones)", n, int64(2))
	edge, err := d.WebmailSessionActive("jti-edge", now)
	mustNoErr(t, "read the session that expired at now", err)
	wantEq(t, "the session that expired at now is active", edge, false)
	liveWeb, err := d.WebmailSessionActive("jti-live", now)
	mustNoErr(t, "read the live webmail session", err)
	wantEq(t, "the live webmail session survived (the sweep must not log anyone out)", liveWeb, true)
	wantRows(t, d.db, "expired webmail rows after the prune", 0, `SELECT COUNT(*) FROM webmail_sessions WHERE jti = ?`, "jti-old")

	an, err := d.PurgeExpiredAdminSessions(now)
	mustNoErr(t, "prune panel sessions", err)
	wantEq(t, "panel sessions pruned (only the expired one)", an, int64(1))
	livePanel, err := d.AdminSessionActive("admin-live", now)
	mustNoErr(t, "read the live panel session", err)
	wantEq(t, "the live panel session survived", livePanel, true)
	wantRows(t, d.db, "expired panel rows after the prune", 0, `SELECT COUNT(*) FROM admin_sessions WHERE jti = ?`, "admin-old")

	// A second pass finds nothing left, so the sweep is idempotent and safe to run
	// from several instances.
	again, err := d.PurgeExpiredWebmailSessions(now)
	mustNoErr(t, "prune webmail sessions again", err)
	wantEq(t, "rows the second pass pruned", again, int64(0))
}
