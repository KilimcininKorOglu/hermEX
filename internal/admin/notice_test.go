package admin

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/logging"
)

// errStorePath is the shape of a real store failure surfacing through a panel: it
// names the mailbox directory on disk.
var errStorePath = errors.New("open /var/lib/hermex/mail/hermex.test/bob/objects.sqlite3: permission denied")

// loggingAdminServerStore is loggingAdminServer with a caller-supplied mailbox store,
// so a test can make a store-backed panel fail.
func loggingAdminServerStore(t *testing.T, d Directory, store MailboxStore) (*httptest.Server, *failCaptureSink) {
	t.Helper()
	sink := &failCaptureSink{}
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = store
	srv.SetLogger(logging.New(sink))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close() })
	return ts, sink
}

// TestPanelNoticeDoesNotLeakStorePath proves a store failure rendered into a panel
// notice carries only the fixed message. The panel is the surface an operator
// actually uses, and an admin-panel account is not necessarily a system
// administrator, so the path on disk must not appear there.
func TestPanelNoticeDoesNotLeakStorePath(t *testing.T) {
	d := systemAdminDir()
	ts, _ := loggingAdminServerStore(t, d, &fakeStore{setErr: errStorePath})
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, http.MethodPut, "/admin/ui/users/bob@hermex.test/delegates",
		session, csrf, url.Values{"delegates": {"carol@hermex.test"}}.Encode())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	for _, leak := range []string{"/var/lib/hermex", "objects.sqlite3", "permission denied"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the panel carries internal detail %q: %s", leak, body)
		}
	}
	if !strings.Contains(string(body), "Could not save delegates") {
		t.Errorf("the panel dropped the handler's own message: %s", body)
	}
}

// TestPanelNoticeRecordsTheRealError proves the sanitized notice does not swallow the
// failure: the full error reaches the central log under the admin subsystem.
func TestPanelNoticeRecordsTheRealError(t *testing.T) {
	d := systemAdminDir()
	ts, sink := loggingAdminServerStore(t, d, &fakeStore{setErr: errStorePath})
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, http.MethodPut, "/admin/ui/users/bob@hermex.test/delegates",
		session, csrf, url.Values{"delegates": {"carol@hermex.test"}}.Encode())
	resp.Body.Close()

	e, ok := sink.find("panel.fail")
	if !ok {
		t.Fatal("the panel failure was not recorded, so the real error is lost")
	}
	if !strings.Contains(e.Err, "objects.sqlite3") {
		t.Errorf("recorded error = %q, want the full store error", e.Err)
	}
	if e.Subsystem != logging.Admin {
		t.Errorf("recorded subsystem = %q, want %q", e.Subsystem, logging.Admin)
	}
	if e.Level != logging.LevelError {
		t.Errorf("recorded level = %v, want error", e.Level)
	}
}

// TestRenderedPanelNoticeDoesNotLeakDriverText covers the other rendered shape, where
// the notice is threaded through a page-data builder rather than assigned into a map.
func TestRenderedPanelNoticeDoesNotLeakDriverText(t *testing.T) {
	d := systemAdminDir()
	d.createErr = errors.New(driverError)
	ts, sink := loggingAdminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/mlists", session, csrf,
		url.Values{"listname": {"team@hermex.test"}, "type": {"1"}, "privilege": {"0"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	for _, leak := range []string{"1062", "Duplicate entry", "users.username"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the panel carries driver text %q: %s", leak, body)
		}
	}
	if !strings.Contains(string(body), "Could not create list") {
		t.Errorf("the panel dropped the handler's own message: %s", body)
	}
	if _, ok := sink.find("panel.fail"); !ok {
		t.Error("the panel failure was not recorded")
	}
}

// TestPanelNoticeClassMatchesTheOutcome proves a settings panel renders its notice
// with the class of what happened. The page announces an "ok" notice as a toast and
// then removes it, so a refusal rendered as "ok" read as a saved setting and was
// gone from the page a few seconds later.
func TestPanelNoticeClassMatchesTheOutcome(t *testing.T) {
	ts, _ := loggingAdminServer(t, systemAdminDir())
	session, csrf := loginCookies(t, ts)

	for _, tc := range []struct {
		name, path string
		form       url.Values
		want       string
	}{
		{"refused", "/admin/ui/limits/requestrate", url.Values{"http_burst": {"0"}, "http_window": {"60"}},
			`<p class="error">Burst and window must each be at least 1`},
		{"rejected upload", "/admin/ui/tls/upload", url.Values{"cert": {"not a certificate"}, "key": {"not a key"}},
			`<p class="error">Upload rejected`},
		{"saved with a caveat", "/admin/ui/antispam/digest",
			url.Values{"enabled": {"1"}, "interval": {"24"}, "base_url": {"https://mail.example.com"}},
			`<p class="warn">Digest settings saved, but`},
		{"saved", "/admin/ui/limits/requestrate", url.Values{"http_burst": {"900"}, "http_window": {"30"}},
			`<p class="ok">Request-rate settings saved`},
	} {
		body := wantBody(t, htmxPOST(t, ts, tc.path, session, csrf, tc.form), http.StatusOK, tc.name)
		wantContains(t, body, tc.want, tc.name+": the notice carries the outcome's class")
	}
}

// TestCertificateUploadKeepsItsValidationMessage proves the sanitization spared the
// panel's own validation. validateTLSCert describes the file the operator just
// uploaded, not server internals, and its message is the only thing telling them what
// is wrong with it.
func TestCertificateUploadKeepsItsValidationMessage(t *testing.T) {
	ts, _ := loggingAdminServer(t, systemAdminDir())
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/tls/upload", session, csrf,
		url.Values{"name": {""}, "cert": {"not a certificate"}, "key": {"not a key"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "Upload rejected") {
		t.Fatalf("a malformed upload was not rejected: %s", body)
	}
	if !strings.Contains(string(body), "not a valid pair") {
		t.Errorf("the rejection no longer says what is wrong with the upload: %s", body)
	}
}
