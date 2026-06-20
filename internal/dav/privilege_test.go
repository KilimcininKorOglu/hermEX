package dav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
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

// TestDAVPrivilegeDenied proves a user with valid credentials but no DAV service
// privilege is refused with 403 — so revoking the privilege actually blocks
// CalDAV/CardDAV access, rather than merely being stored.
func TestDAVPrivilegeDenied(t *testing.T) {
	accs := privDir{
		StaticAccounts: seedMailbox(t),
		privs:          directory.ServicePrivileges{DAV: false, POP3IMAP: true, SMTP: true, Web: true, EAS: true},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	defer ts.Close()

	req, err := http.NewRequest("PROPFIND", ts.URL+"/dav/principals/"+testUser+"/", nil)
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
		t.Fatalf("DAV request with a valid password but no DAV privilege got %d, want 403", resp.StatusCode)
	}
}
