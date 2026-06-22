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
}

// TestSaveLimits proves the form converts the entered MB to bytes and persists it, the
// value the IMAP daemon then polls to apply without a restart.
func TestSaveLimits(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/limits", session, csrf, url.Values{"imap_literal_mb": {"10"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Size limits saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.sizeLimitsFound || d.sizeLimits.IMAPLiteralBytes != 10*1024*1024 {
		t.Errorf("limit not persisted as bytes: found=%v %+v, want 10485760", d.sizeLimitsFound, d.sizeLimits)
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
