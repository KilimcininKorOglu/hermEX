package dav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// hidingAccounts is a directory whose GAL carries the operator's hide mask, which
// the static directory never sets.
type hidingAccounts struct {
	directory.StaticAccounts
	hidden map[string]uint32
}

func (h hidingAccounts) SearchGAL(caller, q string, limit int) ([]directory.GALEntry, error) {
	entries, err := h.StaticAccounts.SearchGAL(caller, q, limit)
	for i := range entries {
		entries[i].HiddenFrom = h.hidden[entries[i].Address]
	}
	return entries, err
}

// TestPrincipalSearchOmitsHiddenUser proves a principal-property-search does not
// return a user the operator hid from the address book, while still returning the
// caller, who is not hidden.
func TestPrincipalSearchOmitsHiddenUser(t *testing.T) {
	accs := hidingAccounts{
		StaticAccounts: directory.StaticAccounts{
			testUser:           {Password: testPass, MailboxPath: t.TempDir()},
			"alex@hermex.test": {Password: testPass, MailboxPath: t.TempDir()},
		},
		hidden: map[string]uint32{"alex@hermex.test": directory.HideFromGAL},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)

	body := `<D:principal-property-search xmlns:D="DAV:">` +
		`<D:property-search><D:prop><D:displayname/></D:prop><D:match>al</D:match></D:property-search>` +
		`<D:prop><D:displayname/></D:prop></D:principal-property-search>`
	resp, out := doFull(t, ts, "REPORT", "/dav/principals/", body, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	if strings.Contains(out, "alex@hermex.test") {
		t.Errorf("principal search returned a user hidden from the address book\n%s", out)
	}
	if !strings.Contains(out, testUser) {
		t.Errorf("principal search dropped the visible user\n%s", out)
	}
}
