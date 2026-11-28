package admin

import (
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
	page := wantBody(t, resp, http.StatusOK, "authed domain detail GET")
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
	// The contact and group lists come from per-domain queries; the page must ask
	// them for this domain, not another.
	wantEq(t, d.contactsDomainID, int64(1), "the domain the contacts were listed for")
	wantEq(t, d.mlistsDomainID, int64(1), "the domain the groups were listed for")
	if !strings.Contains(page, "/admin/ui/public-folders?domain=one.test") {
		t.Errorf("domain detail missing the per-domain public-folders link")
	}
}
