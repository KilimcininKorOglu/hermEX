package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestSettingsMailboxUsage checks the settings page reports mailbox storage usage,
// reflecting the stored messages and a set storage quota.
func TestSettingsMailboxUsage(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "hello", "", "some body content", 100, 0)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{StorageKB: 1024}); err != nil { // 1 MB quota
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	cl := authedClient(t, ts)
	code, body := get(t, cl, ts.URL+"/settings")
	if code != 200 {
		t.Fatalf("settings status %d, want 200", code)
	}
	if !strings.Contains(body, "Mailbox usage") {
		t.Errorf("settings page missing the mailbox usage widget")
	}
	if strings.Contains(body, "0 B of") {
		t.Errorf("usage shows 0 B despite a seeded message")
	}
	if !strings.Contains(body, "of 1.0 MB used") {
		t.Errorf("usage does not show the 1 MB quota:\n%s", body)
	}
}
