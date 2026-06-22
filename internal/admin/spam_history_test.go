package admin

import (
	"io"
	"net/http"
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
}
