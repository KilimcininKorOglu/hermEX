package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// TestSaveRecoverableRetention proves the form persists the entered window, the value the
// admin sweep then polls to purge expired soft-deleted items without a restart.
func TestSaveRecoverableRetention(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/recoverable-retention", "", "", url.Values{"recoverable_retention_days": {"30"}}),
		http.StatusUnauthorized, "unauth recoverable retention save")
	wantFalse(t, d.recoverableFound, "unauth request stored recoverable retention")

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf, url.Values{"recoverable_retention_days": {"30"}}),
		http.StatusOK, "save recoverable retention")
	wantContains(t, body, "Recoverable Items retention saved", "save recoverable retention")
	wantTrue(t, d.recoverableFound, "retention not persisted")
	wantEq(t, d.recoverable.RetentionDays, 30, "retention days")
}

// TestSaveRecoverableRetentionNeedsASystemAdmin proves a domain admin cannot set
// the window that purges every domain's deleted items.
func TestSaveRecoverableRetentionNeedsASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 9, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf,
		url.Values{"recoverable_retention_days": {"1"}}), http.StatusForbidden, "domain-admin retention save")
	wantFalse(t, d.recoverableFound, "a domain admin stored a Recoverable Items window")
}

// TestSaveRecoverableRetentionRefusesWhatIsNotADayCount sends a typo and a negative
// count. Both were read as 0 and silently disabled the purge; now each is refused
// and nothing is stored.
func TestSaveRecoverableRetentionRefusesWhatIsNotADayCount(t *testing.T) {
	for _, bad := range []string{"3O", "-14"} {
		d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		body := wantBody(t, htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf, url.Values{"recoverable_retention_days": {bad}}), http.StatusOK, bad)
		wantContains(t, body, "whole number of days", "the refusal for "+bad)
		wantFalse(t, d.recoverableFound, bad+" was stored")
	}
}

// TestSaveRecoverableRetentionKeepForever proves zero is accepted and persisted as
// keep-forever, disabling auto-purge.
func TestSaveRecoverableRetentionKeepForever(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf, url.Values{"recoverable_retention_days": {"0"}}),
		http.StatusOK, "save keep-forever")
	wantContains(t, body, "keep forever", "save keep-forever")
	wantTrue(t, d.recoverableFound, "keep-forever not persisted")
	wantEq(t, d.recoverable.RetentionDays, 0, "keep-forever retention days")
}
