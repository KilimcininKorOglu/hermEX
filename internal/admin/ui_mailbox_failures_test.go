package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestUIMailboxFailures proves the page lists the mailboxes whose database
// refused a delivery permanently, and that it asks the log store for that one
// event rather than filtering a subsystem's whole traffic.
func TestUIMailboxFailures(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	lr := &fakeLogReader{entries: []logging.LogEntry{{
		Time:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Level: "error", Subsystem: "mta", Name: "delivery.mailbox_unusable",
		User: "alice@hermex.test", RemoteAddr: "1.2.3.4",
		Err: "objectstore: object schema: file is not a database (26)",
	}}}
	ts := adminServerWithLogs(t, d, lr)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/mailbox-failures", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mailbox failures page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "alice@hermex.test") ||
		!strings.Contains(string(body), "not a database") {
		t.Errorf("mailbox failures page missing the recorded failure: %s", body)
	}
	if lr.lastEvent != "delivery.mailbox_unusable" {
		t.Errorf("the page queried event %q, want delivery.mailbox_unusable", lr.lastEvent)
	}
}

// TestUIMailboxFailuresDisabled proves the page reports logging as unconfigured
// when no reader is attached, because the condition is only visible through the
// log store.
func TestUIMailboxFailuresDisabled(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d) // no log reader
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/mailbox-failures", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mailbox failures page status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not configured") {
		t.Errorf("the page should report logging unconfigured: %s", body)
	}
}

// TestUIMailboxFailuresRequiresSystem proves the page is system-admin only: it
// names mailbox paths across every domain.
func TestUIMailboxFailuresRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/mailbox-failures", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin mailbox failures page = %d, want 403", resp.StatusCode)
	}
}
