package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSaveLogRetention proves the form persists the entered window, the value the admin
// daemon then polls to prune the log store without a restart.
func TestSaveLogRetention(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/log-retention", session, csrf, url.Values{"log_retention_days": {"90"}})
	body := readBody(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Log retention saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.logRetentionFound || d.logRetention != 90 {
		t.Errorf("retention not persisted: found=%v days=%d, want 90", d.logRetentionFound, d.logRetention)
	}
}

// TestSaveLogRetentionNeedsASystemAdmin proves a domain admin cannot shorten the
// retention of the one log every domain's activity is recorded in.
func TestSaveLogRetentionNeedsASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 9, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/log-retention", session, csrf, url.Values{"log_retention_days": {"1"}}),
		http.StatusForbidden, "domain-admin retention save")
	wantFalse(t, d.logRetentionFound, "a domain admin stored a retention window")
}

// TestSaveLogRetentionRefusesWhatIsNotADayCount sends a typo and a negative count.
// Both were read as 0, keep forever, and silently turned pruning off; now each is
// refused and nothing is stored.
func TestSaveLogRetentionRefusesWhatIsNotADayCount(t *testing.T) {
	for _, bad := range []string{"9O", "-30"} {
		d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		body := wantBody(t, htmxPOST(t, ts, "/admin/ui/log-retention", session, csrf, url.Values{"log_retention_days": {bad}}), http.StatusOK, bad)
		wantContains(t, body, "whole number of days", "the refusal for "+bad)
		wantFalse(t, d.logRetentionFound, bad+" was stored")
	}
}

// TestSaveLogRetentionKeepForever proves zero is accepted and persisted as keep-forever
// (pruning disabled), the safe default the dangerous "expire everything" path can never
// reach because zero simply means never prune.
func TestSaveLogRetentionKeepForever(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/log-retention", session, csrf, url.Values{"log_retention_days": {"0"}})
	body := readBody(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "keep forever") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging keep-forever", resp.StatusCode, body)
	}
	if !d.logRetentionFound || d.logRetention != 0 {
		t.Errorf("keep-forever not persisted: found=%v days=%d, want 0", d.logRetentionFound, d.logRetention)
	}
}
