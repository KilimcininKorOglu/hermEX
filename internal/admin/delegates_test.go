package admin

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestUIUserDelegates proves the detail-form save writes the delegate list through
// to the mailbox store, trimming each line and dropping blanks, and reports
// success. The list is the same one NSPI serves, so an admin and Outlook manage
// one source, a dropped or mangled entry would desync them.
func TestUIUserDelegates(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/delegates", session, csrf, url.Values{
		"delegates": {"boss@hermex.test\n  assistant@hermex.test  \n\n"},
	}), http.StatusOK, "ui delegates save")
	if store.setDelegatesDir != "/mb/alice" {
		t.Errorf("SetDelegates maildir = %q, want /mb/alice", store.setDelegatesDir)
	}
	if got := store.setDelegatesVal; !slices.Equal(got, []string{"boss@hermex.test", "assistant@hermex.test"}) {
		t.Errorf("stored delegates = %v, want [boss assistant] (trimmed, blanks dropped)", got)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ui delegates save did not report success:\n%s", body)
	}
}

// TestUIUserDelegateAccess proves the panel sets a listed delegate's folder access
// through the store, and refuses an access level outside none/read/write and an
// address that is not on the delegate list, so the form can neither invent a
// level nor hand folder rights to someone the list does not name.
func TestUIUserDelegateAccess(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{delegates: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)
	put := func(grantee, access string) string {
		return wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/delegate-access", session, csrf,
			url.Values{"grantee": {grantee}, "access": {access}}), http.StatusOK, "delegate access "+grantee+"="+access)
	}

	if body := put("Boss@hermex.test", "write"); !strings.Contains(body, "Saved") {
		t.Errorf("listed delegate, write: no success notice:\n%s", body)
	}
	if got := store.delegateAccess["/mb/alice|Boss@hermex.test"]; got != objectstore.DelegateWrite {
		t.Errorf("stored access = %q, want write", got)
	}
	put("stranger@hermex.test", "write")
	if _, set := store.delegateAccess["/mb/alice|stranger@hermex.test"]; set {
		t.Error("an address off the delegate list was given folder access")
	}
	put("Boss@hermex.test", "owner")
	if got := store.delegateAccess["/mb/alice|Boss@hermex.test"]; got != objectstore.DelegateWrite {
		t.Errorf("an unknown level changed the stored access to %q", got)
	}
}

// TestUIUserDelegateAccessNeedsASystemAdmin proves a domain administrator cannot
// set folder access: it opens another person's calendar and contacts.
func TestUIUserDelegateAccessNeedsASystemAdmin(t *testing.T) {
	d := oofUserDir()
	d.roles = nil
	d.perms = []directory.Permission{{Name: directory.PermDomainAdmin, Params: "*"}}
	store := &fakeStore{delegates: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/delegate-access", session, csrf,
		url.Values{"grantee": {"boss@hermex.test"}, "access": {"write"}}), http.StatusForbidden, "delegate access save")
	if len(store.delegateAccess) != 0 {
		t.Errorf("a domain admin set folder access: %v", store.delegateAccess)
	}
}

// TestUIUserDelegatesNeedASystemAdmin proves a domain administrator cannot name
// a mailbox's delegates: a delegate reads and sends as the owner, so the list
// grants mailbox access.
func TestUIUserDelegatesNeedASystemAdmin(t *testing.T) {
	d := oofUserDir()
	d.roles = nil
	d.perms = []directory.Permission{{Name: directory.PermDomainAdmin, Params: "*"}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/delegates", session, csrf,
		url.Values{"delegates": {"intruder@hermex.test"}}), http.StatusForbidden, "delegates save")
	if store.setDelegatesDir != "" || store.setDelegatesVal != nil {
		t.Errorf("a domain admin stored delegates %v on %q", store.setDelegatesVal, store.setDelegatesDir)
	}
}

// TestUIUserDetailShowsDelegates proves the detail page renders the delegates
// section populated from the mailbox store, so an admin sees the current list.
func TestUIUserDetailShowsDelegates(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{delegates: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "user detail page")
	for _, want := range []string{"<h2>Delegates</h2>", `name="delegates"`, "boss@hermex.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page delegates section missing %q", want)
		}
	}
}
