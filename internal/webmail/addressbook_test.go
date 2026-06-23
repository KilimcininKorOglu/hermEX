package webmail

import (
	"strings"
	"testing"
)

// TestAddressBookSearch checks the GAL browser lists directory entries matching a
// search and nothing for a non-matching query.
func TestAddressBookSearch(t *testing.T) {
	path := emptyMailbox(t)
	ts, cl := sharingServer(t, path) // alice (session) + bob in the directory

	code, body := get(t, cl, ts.URL+"/addressbook?q=bob")
	if code != 200 {
		t.Fatalf("addressbook status %d, want 200", code)
	}
	if !strings.Contains(body, "bob@hermex.test") {
		t.Errorf("address book did not list the matching entry:\n%s", body)
	}

	_, empty := get(t, cl, ts.URL+"/addressbook?q=zzzznomatch")
	if strings.Contains(empty, "bob@hermex.test") {
		t.Errorf("address book listed a non-matching entry")
	}
}
