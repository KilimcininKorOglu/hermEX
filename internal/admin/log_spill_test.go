package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// TestSaveLogSpillLimit proves the form persists the cap every daemon then polls,
// shows the stored value on the Settings page, and accepts 0 as no cap.
func TestSaveLogSpillLimit(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/log-spill", session, csrf, url.Values{"log_spill_limit_mb": {"300"}}), http.StatusOK, "save")
	wantContains(t, body, "Spill limit saved", "the save acknowledgement")
	if !d.logSpillSet || d.logSpillMB != 300 {
		t.Fatalf("cap not persisted: set=%v mb=%d, want 300", d.logSpillSet, d.logSpillMB)
	}
	page := wantBody(t, authedReq(t, ts, "GET", "/admin/ui/settings", session, csrf, ""), http.StatusOK, "settings page")
	wantContains(t, page, `name="log_spill_limit_mb" value="300"`, "the stored cap on the Settings page")

	body = wantBody(t, htmxPOST(t, ts, "/admin/ui/log-spill", session, csrf, url.Values{"log_spill_limit_mb": {"0"}}), http.StatusOK, "save 0")
	wantContains(t, body, "Spill limit removed", "the no-limit acknowledgement")
	if d.logSpillMB != 0 {
		t.Errorf("cap = %d after saving 0, want 0", d.logSpillMB)
	}
}

// TestSaveLogSpillLimitRefusesWhatIsNotAMiBCount sends a typo and a negative
// value. Each is refused and nothing is stored, so a typo never lifts the cap.
func TestSaveLogSpillLimitRefusesWhatIsNotAMiBCount(t *testing.T) {
	d0 := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts0 := adminServer(t, d0)
	_, csrf0 := loginCookies(t, ts0)
	wantStatus(t, htmxPOST(t, ts0, "/admin/ui/log-spill", "", csrf0, url.Values{"log_spill_limit_mb": {"300"}}), http.StatusUnauthorized, "unauth log-spill save")
	wantFalse(t, d0.logSpillSet, "unauth request persisted cap")

	for _, bad := range []string{"1O24", "-5"} {
		d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		body := wantBody(t, htmxPOST(t, ts, "/admin/ui/log-spill", session, csrf, url.Values{"log_spill_limit_mb": {bad}}), http.StatusOK, bad)
		wantContains(t, body, "whole number of MiB", "the refusal for "+bad)
		wantFalse(t, d.logSpillSet, bad+" was stored")
	}
}
