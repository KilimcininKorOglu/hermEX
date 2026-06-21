package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestDomainDetailScopedLists proves the domain detail page lists the domain's
// own users, contacts, and groups and does not leak another domain's users.
func TestDomainDetailScopedLists(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "one.test"},
		users: []directory.UserInfo{
			{Username: "a@one.test", DomainID: 1},
			{Username: "b@two.test", DomainID: 2},
		},
		contacts: []directory.ContactInfo{{Address: "c@one.test", DisplayName: "Contact One"}},
		mlists:   []directory.MListInfo{{Listname: "list@one.test"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/domains/1", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	if !strings.Contains(page, "a@one.test") {
		t.Errorf("domain detail missing the domain's own user a@one.test")
	}
	if strings.Contains(page, "b@two.test") {
		t.Errorf("domain detail leaked another domain's user b@two.test")
	}
	if !strings.Contains(page, "c@one.test") {
		t.Errorf("domain detail missing the domain's contact")
	}
	if !strings.Contains(page, "list@one.test") {
		t.Errorf("domain detail missing the domain's group")
	}
}
