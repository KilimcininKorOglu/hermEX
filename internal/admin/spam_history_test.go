package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestSpamHistoryPageRenders proves the Spam History page renders the recorded
// verdicts for a system admin, showing the sender and the reasons each scored.
func TestSpamHistoryPageRenders(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		verdicts: []directory.SpamVerdict{{
			Time: 1700000000, MailFrom: "bad@spam.example", RemoteAddr: "203.0.113.9",
			Score: 12, Spam: true, Reasons: "SPF fail; listed on DNSBL zen.example",
		}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/spam-history", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spam-history page = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "Spam History") || !strings.Contains(page, "bad@spam.example") ||
		!strings.Contains(page, "listed on DNSBL zen.example") {
		t.Errorf("spam-history page missing expected verdict rows:\n%s", page)
	}
	// The retention panel shows the built-in default until one is saved.
	if !strings.Contains(page, "Retention") || !strings.Contains(page, "10000") {
		t.Errorf("spam-history page missing the retention panel default:\n%s", page)
	}
}

// TestSaveSpamRetention proves the retention form persists the bound and acknowledges
// the save, the value the MTA then polls to keep the spam_history table without a
// restart.
func TestSaveSpamRetention(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/spam-history/retention", session, csrf, url.Values{"retain": {"500"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Retention saved") {
		t.Errorf("response missing acknowledgment:\n%s", body)
	}
	if !d.spamHistoryFound || d.spamHistory.Retain != 500 {
		t.Errorf("retention not persisted as entered: found=%v %+v", d.spamHistoryFound, d.spamHistory)
	}
}

// TestSaveSpamRetentionRejectsBadValues proves a retention below 1 (which would prune
// the table to nothing on the next insert) is rejected and nothing is persisted.
func TestSaveSpamRetentionRejectsBadValues(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/spam-history/retention", session, csrf, url.Values{"retain": {"0"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "at least 1") {
		t.Errorf("expected a validation message:\n%s", body)
	}
	if d.spamHistoryFound {
		t.Error("invalid retention must not be persisted")
	}
}
