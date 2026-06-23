package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestViewHeaders checks that /headers shows the served message's header block
// (with the Subject line) and not the body, which it deliberately strips off.
func TestViewHeaders(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "header probe", "rcpt@hermex.test", "UNIQUEBODYMARKER", 100, 0)
	ts := newTestServer(t, path)
	cl := authedClient(t, ts)

	code, body := get(t, cl, ts.URL+"/headers?folder=INBOX&uid="+itoa(uid))
	if code != 200 {
		t.Fatalf("headers status %d, want 200", code)
	}
	if !strings.Contains(body, "Internet headers") || !strings.Contains(body, "header probe") {
		t.Errorf("headers page missing the title or subject:\n%s", body)
	}
	if !strings.Contains(body, "Subject:") {
		t.Errorf("headers page missing the Subject header line:\n%s", body)
	}
	if strings.Contains(body, "UNIQUEBODYMARKER") {
		t.Errorf("headers page leaked the message body (only the header block should show)")
	}
}
