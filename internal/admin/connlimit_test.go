package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLimitsPageRendersConnLimit proves the connection-cap panel appears with the
// limiter's own built-in values until an operator saves one, so the numbers shown
// are the numbers actually in force.
func TestLimitsPageRendersConnLimit(t *testing.T) {
	d := systemAdmin()
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/limits", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if !strings.Contains(page, `id="conn-limit-panel"`) {
		t.Fatalf("the connection-cap panel is missing:\n%s", page)
	}
	if !strings.Contains(page, `name="conn_max_total" value="1000"`) ||
		!strings.Contains(page, `name="conn_max_per_client" value="20"`) {
		t.Errorf("the panel does not show the built-in defaults:\n%s", page)
	}
}

// TestSaveConnLimit proves the form persists the toggle and both caps, the values
// every IMAP, POP3 and SMTP daemon then polls to apply without a restart.
func TestSaveConnLimit(t *testing.T) {
	d := systemAdmin()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits/connections", session, csrf,
		url.Values{"enabled": {"1"}, "conn_max_total": {"400"}, "conn_max_per_client": {"8"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Connection caps saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
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

	resp := htmxPOST(t, ts, "/admin/ui/limits/connections", session, csrf,
		url.Values{"enabled": {"1"}, "conn_max_total": {"0"}, "conn_max_per_client": {"8"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.connLimitFound {
		t.Error("invalid connection caps must not be persisted")
	}
}
