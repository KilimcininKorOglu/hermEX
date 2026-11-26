package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSaveRecoverableRetention proves the form persists the entered window, the value the
// admin sweep then polls to purge expired soft-deleted items without a restart.
func TestSaveRecoverableRetention(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf, url.Values{"recoverable_retention_days": {"30"}})
	body := readBody(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Recoverable Items retention saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.recoverableFound || d.recoverable.RetentionDays != 30 {
		t.Errorf("retention not persisted: found=%v days=%d, want 30", d.recoverableFound, d.recoverable.RetentionDays)
	}
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

	resp := htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf, url.Values{"recoverable_retention_days": {"0"}})
	body := readBody(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "keep forever") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging keep-forever", resp.StatusCode, body)
	}
	if !d.recoverableFound || d.recoverable.RetentionDays != 0 {
		t.Errorf("keep-forever not persisted: found=%v days=%d, want 0", d.recoverableFound, d.recoverable.RetentionDays)
	}
}
