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

	page := wantBody(t, authedGET(t, ts, "/admin/ui/limits", session), http.StatusOK, "limits page")
	for _, want := range []string{
		"Protocol size limits", "IMAP maximum literal", `value="50"`,
		"EWS maximum SOAP request", `value="8"`,
		"ActiveSync maximum request", `value="4"`,
		"CalDAV maximum iCalendar", "CardDAV maximum vCard",
		"Webmail maximum request", `name="webmail_request_mb" value="40"`,
		"MAPI/HTTP maximum request", `name="mapi_request_mb" value="32"`,
	} {
		wantContains(t, page, want, "the limits page carries its fields and built-in defaults")
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
		"mapi_request_mb": {"16"}, "freebusy_max_targets": {"25"}, "webmail_preview_mb": {"6"}, "tlsrpt_mb": {"7"},
		"imap_line_bytes": {"32768"}, "pop3_line_bytes": {"4096"}, "smtp_line_bytes": {"1024"},
		"ews_subscription_timeout_min": {"720"},
	})
	body := wantBody(t, resp, http.StatusOK, "save limits")
	wantContains(t, body, "Size limits saved", "the save is acknowledged")

	wantTrue(t, d.sizeLimitsFound, "the limits reach the directory")
	wantEq(t, d.sizeLimits.IMAPLiteralBytes, int64(10*1024*1024), "IMAP literal bytes")
	wantEq(t, d.sizeLimits.EWSRequestBytes, int64(4*1024*1024), "EWS request bytes")
	wantEq(t, d.sizeLimits.ActiveSyncRequestBytes, int64(2*1024*1024), "ActiveSync request bytes")
	wantEq(t, d.sizeLimits.DAVICalBytes, int64(3*1024*1024), "DAV iCalendar bytes")
	wantEq(t, d.sizeLimits.DAVVCardBytes, int64(5*1024*1024), "DAV vCard bytes")
	wantEq(t, d.sizeLimits.WebmailRequestBytes, int64(20*1024*1024), "webmail request bytes")
	wantEq(t, d.sizeLimits.MapiRequestBytes, int64(16*1024*1024), "MAPI request bytes")
	wantEq(t, d.sizeLimits.WebmailPreviewMaxBytes, int64(6*1024*1024), "inline preview bytes")
	wantEq(t, d.sizeLimits.TLSReportBytes, int64(7*1024*1024), "TLS report bytes")
	// The free/busy cap is a count, so it must persist unscaled by the megabyte factor.
	wantEq(t, d.sizeLimits.FreeBusyMaxTargets, int64(25), "free/busy target cap")
	// The command-line caps are byte counts, so they persist unscaled too.
	wantEq(t, d.sizeLimits.IMAPCommandLineBytes, int64(32768), "IMAP command-line bytes")
	wantEq(t, d.sizeLimits.POP3CommandLineBytes, int64(4096), "POP3 command-line bytes")
	wantEq(t, d.sizeLimits.SMTPCommandLineBytes, int64(1024), "SMTP command-line bytes")
	// The subscription timeout is a duration in minutes, so it persists unscaled too.
	wantEq(t, d.sizeLimits.EWSSubscriptionTimeoutMinutes, int64(720), "EWS subscription timeout minutes")
}

// TestSaveLimitsRejectsATinyCommandLine proves a cap no command fits in is rejected
// and nothing persists, because such a daemon would refuse every client.
func TestSaveLimitsRejectsATinyCommandLine(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits", session, csrf, url.Values{
		"imap_literal_mb": {"10"}, "ews_request_mb": {"4"}, "activesync_request_mb": {"2"},
		"dav_ical_mb": {"3"}, "dav_vcard_mb": {"5"}, "webmail_request_mb": {"20"},
		"mapi_request_mb": {"16"}, "freebusy_max_targets": {"25"}, "webmail_preview_mb": {"6"}, "tlsrpt_mb": {"7"},
		"imap_line_bytes": {"8"}, "pop3_line_bytes": {"4096"}, "smtp_line_bytes": {"1024"},
		"ews_subscription_timeout_min": {"30"},
	})
	body := wantBody(t, resp, http.StatusOK, "save limits with a tiny command-line cap")

	wantContains(t, body, "at least 64 bytes", "the refusal names the floor")
	wantFalse(t, d.sizeLimitsFound, "nothing is persisted")
}

// TestSaveLimitsRejectsAnOutOfRangeSubscriptionTimeout proves the subscription
// idle timeout is bounded on the way in. [MS-OXWSNTIF] 2.2.4.24 caps it at 1440
// minutes, and the value also rides in the SubscriptionId as a 32-bit field, so a
// typo must be refused rather than stored.
func TestSaveLimitsRejectsAnOutOfRangeSubscriptionTimeout(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits", session, csrf, url.Values{
		"imap_literal_mb": {"10"}, "ews_request_mb": {"4"}, "activesync_request_mb": {"2"},
		"dav_ical_mb": {"3"}, "dav_vcard_mb": {"5"}, "webmail_request_mb": {"20"},
		"mapi_request_mb": {"16"}, "freebusy_max_targets": {"25"}, "webmail_preview_mb": {"6"}, "tlsrpt_mb": {"7"},
		"imap_line_bytes": {"32768"}, "pop3_line_bytes": {"4096"}, "smtp_line_bytes": {"1024"},
		"ews_subscription_timeout_min": {"5000"},
	})
	body := wantBody(t, resp, http.StatusOK, "save limits with an out-of-range subscription timeout")

	wantContains(t, body, "between 1 and 1440 minutes", "the refusal names the bound")
	wantFalse(t, d.sizeLimitsFound, "nothing is persisted")
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

// TestLimitsPageRendersLoginLockout proves the login-lockout panel appears on the
// Limits page with the limiter's own built-in tuning until an operator saves one,
// so the numbers shown are the numbers actually in force.
func TestLimitsPageRendersLoginLockout(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/limits", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if !strings.Contains(page, "Login lockout") {
		t.Errorf("login-lockout panel missing:\n%s", page)
	}
	if !strings.Contains(page, `name="login_max_fails" value="5"`) ||
		!strings.Contains(page, `name="login_window" value="900"`) ||
		!strings.Contains(page, `name="login_lockout" value="900"`) {
		t.Errorf("login-lockout panel missing the built-in tuning:\n%s", page)
	}
}

// TestSaveLoginLockout proves the tuning persists, the whole point of the change:
// it used to live in package constants that every call site took blind, so an
// operator facing a credential-stuffing wave could only tighten the threshold by
// editing source and rebuilding the affected daemon.
func TestSaveLoginLockout(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits/loginlockout", session, csrf,
		url.Values{"login_max_fails": {"3"}, "login_window": {"300"}, "login_lockout": {"1800"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Login-lockout settings saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	want := directory.LoginLockoutSettings{MaxFails: 3, WindowSeconds: 300, LockoutSeconds: 1800}
	if !d.loginLockoutFound || d.loginLockout != want {
		t.Errorf("settings not persisted as entered: found=%v %+v", d.loginLockoutFound, d.loginLockout)
	}
	// The panel has to come back showing what was saved, or the operator cannot tell
	// a save apart from a no-op.
	if !strings.Contains(string(body), `name="login_max_fails" value="3"`) {
		t.Errorf("the panel does not reflect the saved tuning:\n%s", body)
	}
}

// TestSaveLoginLockoutRejectsBadValues proves a value below 1 is refused. A
// threshold of zero locks out every login on the daemon at the first failure, and
// with the panel itself behind the same limiter that is an operator locking
// themselves out of the page that would undo it.
func TestSaveLoginLockoutRejectsBadValues(t *testing.T) {
	for _, bad := range []url.Values{
		{"login_max_fails": {"0"}, "login_window": {"900"}, "login_lockout": {"900"}},
		{"login_max_fails": {"5"}, "login_window": {"0"}, "login_lockout": {"900"}},
		{"login_max_fails": {"5"}, "login_window": {"900"}, "login_lockout": {"-1"}},
	} {
		d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)

		resp := htmxPOST(t, ts, "/admin/ui/limits/loginlockout", session, csrf, bad)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "at least 1") {
			t.Errorf("%v: expected a validation message:\n%s", bad, body)
		}
		if d.loginLockoutFound {
			t.Errorf("%v: invalid login-lockout settings must not be persisted", bad)
		}
	}
}

// TestFetchPolicyIsSystemAdminOnly proves the source-address switch is a full
// system-administrator decision and round-trips through the store. Domain admins
// create the fetchmail entries the block guards, so if they could also lift the
// block it would guard nothing.
func TestFetchPolicyIsSystemAdminOnly(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// The page renders the switch, off by default.
	req, _ := http.NewRequest("GET", ts.URL+"/admin/ui/limits", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), `name="fetch_allow_internal"`) {
		t.Errorf("limits page missing the fetch policy switch:\n%s", page)
	}
	if strings.Contains(string(page), `name="fetch_allow_internal" value="1" checked`) {
		t.Errorf("the switch renders as on by default; internal sources must be refused until allowed")
	}

	// A system admin can turn it on.
	saved := htmxPOST(t, ts, "/admin/ui/limits/fetchpolicy", session, csrf, url.Values{"fetch_allow_internal": {"1"}})
	body, _ := io.ReadAll(saved.Body)
	saved.Body.Close()
	if saved.StatusCode != http.StatusOK || !strings.Contains(string(body), "Fetch policy saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", saved.StatusCode, body)
	}
	if !d.fetchSettingsFound || !d.fetchSettings.AllowInternalSources {
		t.Errorf("policy not persisted: found=%v %+v", d.fetchSettingsFound, d.fetchSettings)
	}

	// Clearing the box turns it back off (an unchecked checkbox posts nothing).
	htmxPOST(t, ts, "/admin/ui/limits/fetchpolicy", session, csrf, url.Values{}).Body.Close()
	if d.fetchSettings.AllowInternalSources {
		t.Error("clearing the switch left internal sources allowed")
	}
}

// TestFetchPolicyRefusesADomainAdmin is the security half: the role that can
// create a fetchmail entry must not be able to lift the address block.
func TestFetchPolicyRefusesADomainAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 9, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits/fetchpolicy", session, csrf, url.Values{"fetch_allow_internal": {"1"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a domain-scoped admin", resp.StatusCode)
	}
	if d.fetchSettings.AllowInternalSources {
		t.Error("a domain-scoped admin lifted the internal-address block")
	}
}
