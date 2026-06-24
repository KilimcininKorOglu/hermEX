package admin

import (
	"io"
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Recoverable Items retention saved") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging the save", resp.StatusCode, body)
	}
	if !d.recoverableFound || d.recoverable.RetentionDays != 30 {
		t.Errorf("retention not persisted: found=%v days=%d, want 30", d.recoverableFound, d.recoverable.RetentionDays)
	}
}

// TestSaveRecoverableRetentionKeepForever proves zero is accepted and persisted as
// keep-forever, disabling auto-purge.
func TestSaveRecoverableRetentionKeepForever(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/recoverable-retention", session, csrf, url.Values{"recoverable_retention_days": {"0"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "keep forever") {
		t.Fatalf("save = %d body=%q, want 200 acknowledging keep-forever", resp.StatusCode, body)
	}
	if !d.recoverableFound || d.recoverable.RetentionDays != 0 {
		t.Errorf("keep-forever not persisted: found=%v days=%d, want 0", d.recoverableFound, d.recoverable.RetentionDays)
	}
}
