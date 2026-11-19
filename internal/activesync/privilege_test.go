package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// privDir wraps a static directory but reports a fixed privilege set, so a test
// can deny a service to an otherwise-valid account. unknown makes the lookup
// answer that it could not tell.
type privDir struct {
	directory.StaticAccounts
	privs   directory.ServicePrivileges
	unknown bool
}

func (d privDir) Privileges(user string) (directory.ServicePrivileges, bool) {
	if _, ok := d.StaticAccounts[strings.ToLower(user)]; !ok || d.unknown {
		return directory.ServicePrivileges{}, false
	}
	return d.privs, true
}

// privilegeStatus sends one FolderSync with the given password to a server over
// dir and returns the HTTP status.
func privilegeStatus(t *testing.T, accs privDir, pass string) int {
	t.Helper()
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	defer ts.Close()
	u := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=FolderSync&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
	body := wbxml.Marshal(wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	req, err := http.NewRequest("POST", u, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, pass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestActiveSyncPrivilegeDenied proves a user with valid credentials but no EAS
// service privilege is refused with 403 before any command runs, so revoking the
// privilege actually blocks the device rather than merely being stored. A lookup
// that cannot answer denies too (fail closed), and a wrong password is a 401
// whatever the privilege: a caller who has not authenticated must not learn that
// the account exists with EAS switched off.
func TestActiveSyncPrivilegeDenied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "u")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	static := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	noEAS := directory.ServicePrivileges{EAS: false, POP3IMAP: true, SMTP: true, Web: true, DAV: true}

	for _, tc := range []struct {
		name string
		accs privDir
		pass string
		want int
	}{
		{"privilege revoked", privDir{StaticAccounts: static, privs: noEAS}, testPass, http.StatusForbidden},
		{"privilege unknown", privDir{StaticAccounts: static, privs: directory.ServicePrivileges{EAS: true}, unknown: true}, testPass, http.StatusForbidden},
		{"wrong password", privDir{StaticAccounts: static, privs: noEAS}, "wrong", http.StatusUnauthorized},
		// The control: the same account with EAS granted is served, so the 403s
		// above come from the privilege, not from a server that refuses everyone.
		{"privilege granted", privDir{StaticAccounts: static, privs: directory.ServicePrivileges{EAS: true}}, testPass, http.StatusOK},
	} {
		if got := privilegeStatus(t, tc.accs, tc.pass); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}
}
