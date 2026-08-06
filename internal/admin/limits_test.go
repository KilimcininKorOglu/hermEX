package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestLimitsPageRenders proves the Limits page renders for a system admin with the
// built-in IMAP literal default until one is saved.
func TestLimitsPageRenders(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/limits", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("limits page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "Protocol size limits") || !strings.Contains(page, "IMAP maximum literal") || !strings.Contains(page, "value=\"50\"") {
		t.Errorf("limits page missing expected content/default:\n%s", page)
	}
	if !strings.Contains(page, "EWS maximum SOAP request") || !strings.Contains(page, "value=\"8\"") {
		t.Errorf("limits page missing the EWS limit/default:\n%s", page)
	}
	if !strings.Contains(page, "ActiveSync maximum request") || !strings.Contains(page, "value=\"4\"") {
		t.Errorf("limits page missing the ActiveSync limit/default:\n%s", page)
	}
	if !strings.Contains(page, "CalDAV maximum iCalendar") || !strings.Contains(page, "CardDAV maximum vCard") {
		t.Errorf("limits page missing the DAV limits:\n%s", page)
	}
	if !strings.Contains(page, "Webmail maximum request") || !strings.Contains(page, `name="webmail_request_mb" value="40"`) {
		t.Errorf("limits page missing the webmail limit/default:\n%s", page)
	}
}

// TestSaveLimits proves the form converts the entered MB to bytes and persists it, the
// value the IMAP daemon then polls to apply without a restart.
func TestSaveLimits(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits", session, csrf, url.Values{
		"imap_literal_mb": {"10"}, "ews_request_mb": {"4"}, "activesync_request_mb": {"2"},
		"dav_ical_mb": {"3"}, "dav_vcard_mb": {"5"}, "webmail_request_mb": {"20"},
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Size limits saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.sizeLimitsFound || d.sizeLimits.IMAPLiteralBytes != 10*1024*1024 || d.sizeLimits.EWSRequestBytes != 4*1024*1024 ||
		d.sizeLimits.ActiveSyncRequestBytes != 2*1024*1024 || d.sizeLimits.DAVICalBytes != 3*1024*1024 ||
		d.sizeLimits.DAVVCardBytes != 5*1024*1024 || d.sizeLimits.WebmailRequestBytes != 20*1024*1024 {
		t.Errorf("limits not persisted as bytes: found=%v %+v", d.sizeLimitsFound, d.sizeLimits)
	}
}

// TestSaveLimitsRejectsBadValues proves a sub-1 MB limit is rejected and nothing persists.
func TestSaveLimitsRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits", session, csrf, url.Values{"imap_literal_mb": {"0"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1 MB") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.sizeLimitsFound {
		t.Error("invalid limit must not be persisted")
	}
}

// TestLimitsPageRendersRequestRate proves the Limits page shows the request-rate panel
// with the limiter's built-in defaults, off, until an operator saves settings.
func TestLimitsPageRendersRequestRate(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/limits", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if !strings.Contains(page, "Request rate limiting is <strong>off</strong>") {
		t.Errorf("request-rate panel missing or not reported as off:\n%s", page)
	}
	if !strings.Contains(page, `name="http_burst" value="600"`) || !strings.Contains(page, `name="http_window" value="60"`) {
		t.Errorf("request-rate panel missing the built-in defaults:\n%s", page)
	}
}

// TestSaveHTTPRateLimit proves the request-rate form persists the toggle, burst and
// window, the values every HTTP daemon then polls to apply without a restart.
func TestSaveHTTPRateLimit(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits/requestrate", session, csrf,
		url.Values{"enabled": {"1"}, "http_burst": {"900"}, "http_window": {"30"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Request-rate settings saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.httpRateLimitFound || !d.httpRateLimit.Enabled || d.httpRateLimit.Burst != 900 || d.httpRateLimit.WindowSeconds != 30 {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.httpRateLimitFound, d.httpRateLimit)
	}
}

// TestSaveHTTPRateLimitRejectsBadValues proves a burst or window below 1 (which would
// admit no requests or collapse the window) is rejected and nothing is persisted.
func TestSaveHTTPRateLimitRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits/requestrate", session, csrf,
		url.Values{"enabled": {"1"}, "http_burst": {"0"}, "http_window": {"60"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.httpRateLimitFound {
		t.Error("invalid request-rate settings must not be persisted")
	}
}
