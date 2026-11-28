package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/connlimit"
)

// TestLimitsPageRendersConnLimit proves the connection-cap panel appears with the
// limiter's own built-in values until an operator saves one, so the numbers shown
// are the numbers actually in force.
func TestLimitsPageRendersConnLimit(t *testing.T) {
	d := systemAdmin()
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/limits", session), http.StatusOK, "limits page")
	if !strings.Contains(page, `id="conn-limit-panel"`) {
		t.Fatalf("the connection-cap panel is missing:\n%s", page)
	}
	if !strings.Contains(page, fmt.Sprintf(`name="conn_max_total" value="%d"`, connlimit.DefaultMaxTotal)) ||
		!strings.Contains(page, fmt.Sprintf(`name="conn_max_per_client" value="%d"`, connlimit.DefaultMaxPerClient)) {
		t.Errorf("the panel does not show the built-in defaults:\n%s", page)
	}
}

// TestSaveConnLimit proves the form persists the toggle and both caps, the values
// every IMAP, POP3 and SMTP daemon then polls to apply without a restart.
func TestSaveConnLimit(t *testing.T) {
	d := systemAdmin()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/limits/connections", "", csrf,
		url.Values{"enabled": {"1"}, "conn_max_total": {"400"}, "conn_max_per_client": {"8"}}),
		http.StatusUnauthorized, "unauth connlimit save")
	wantFalse(t, d.connLimitFound, "unauth save persisted conn limit")

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/limits/connections", session, csrf,
		url.Values{"enabled": {"1"}, "conn_max_total": {"400"}, "conn_max_per_client": {"8"}}),
		http.StatusOK, "save conn limit")
	wantContains(t, body, "Connection caps saved", "save conn limit")
	if !d.connLimitFound || !d.connLimit.Enabled || d.connLimit.MaxTotal != 400 || d.connLimit.MaxPerClient != 8 {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.connLimitFound, d.connLimit)
	}
}

// TestSaveConnLimitRejectsBadValues proves a cap below 1 (which would admit no
// connection at all) is rejected and nothing is persisted.
func TestSaveConnLimitRejectsBadValues(t *testing.T) {
	d := systemAdmin()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, tc := range []struct{ total, perClient string }{
		{"0", "8"}, {"400", "0"}, {"-5", "8"}, {"400", "abc"}, {"", "8"},
	} {
		body := wantBody(t, htmxPOST(t, ts, "/admin/ui/limits/connections", session, csrf,
			url.Values{"enabled": {"1"}, "conn_max_total": {tc.total}, "conn_max_per_client": {tc.perClient}}),
			http.StatusOK, tc.total+"/"+tc.perClient)
		if !strings.Contains(body, "at least 1") {
			t.Errorf("%q/%q: expected a validation message:\n%s", tc.total, tc.perClient, body)
		}
		if d.connLimitFound {
			t.Errorf("%q/%q: invalid connection caps were persisted: %+v", tc.total, tc.perClient, d.connLimit)
		}
	}
}
