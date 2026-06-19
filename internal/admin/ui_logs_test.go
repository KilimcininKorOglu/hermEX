package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// fakeLogReader is a scripted LogReader for the log-viewer tests.
type fakeLogReader struct {
	entries []logging.LogEntry
	err     error
}

func (f *fakeLogReader) Recent(context.Context, string, int64) ([]logging.LogEntry, error) {
	return f.entries, f.err
}

func adminServerWithLogs(t *testing.T, d Directory, lr LogReader) *httptest.Server {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.SetLogReader(lr)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestUILogs proves the log viewer renders recent events for a system admin.
func TestUILogs(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	lr := &fakeLogReader{entries: []logging.LogEntry{{
		Time:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Level: "info", Subsystem: "imap", Name: "auth.ok",
		User: "boss@hermex.test", RemoteAddr: "1.2.3.4",
	}}}
	ts := adminServerWithLogs(t, d, lr)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/logs", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "auth.ok") || !strings.Contains(string(body), "imap") ||
		!strings.Contains(string(body), "1.2.3.4") {
		t.Errorf("logs page missing the event details: %s", body)
	}
}

// TestUILogsDisabled proves the viewer reports logging as unconfigured when no
// reader is attached.
func TestUILogsDisabled(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d) // no log reader
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/logs", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not configured") {
		t.Errorf("logs page should report logging unconfigured: %s", body)
	}
}

// TestUILogsRequiresSystem proves the log viewer is system-admin only.
func TestUILogsRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/logs", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin logs page = %d, want 403", resp.StatusCode)
	}
}
