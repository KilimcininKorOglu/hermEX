package activesync

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// privDir wraps a static directory but reports a fixed privilege set, so a test
// can deny a service to an otherwise-valid account.
type privDir struct {
	directory.StaticAccounts
	privs directory.ServicePrivileges
}

func (d privDir) Privileges(user string) (directory.ServicePrivileges, bool) {
	if _, ok := d.StaticAccounts[strings.ToLower(user)]; !ok {
		return directory.ServicePrivileges{}, false
	}
	return d.privs, true
}

// TestActiveSyncPrivilegeDenied proves a user with valid credentials but no EAS
// service privilege is refused with 403 before any command runs — so revoking
// the privilege actually blocks the device, rather than merely being stored.
func TestActiveSyncPrivilegeDenied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "u")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	accs := privDir{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}},
		privs:          directory.ServicePrivileges{EAS: false, POP3IMAP: true, SMTP: true, Web: true, DAV: true},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	defer ts.Close()

	u := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=FolderSync&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
	req, err := http.NewRequest("POST", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("EAS request with a valid password but no EAS privilege got %d, want 403", resp.StatusCode)
	}
}
