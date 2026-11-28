package admin

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAdminGetUserSendAs proves a system admin reads a user's send-as list.
func TestAdminGetUserSendAs(t *testing.T) {
	store := &fakeStore{sendAs: map[string][]string{"/mb/alice": {"bob@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/users/alice@hermex.test/sendas", session), http.StatusOK, "get sendas")
	if !strings.Contains(body, `"data":["bob@hermex.test"]`) {
		t.Errorf("send-as body = %s, want the grantee list", body)
	}
}

// TestAdminSetUserSendAs proves a system admin writes the send-as list, stored in the
// lowercased canonical form the MTA compares against.
func TestAdminSetUserSendAs(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store) // resolves any grantee
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/sendas", session, csrf,
		`["BOB@Hermex.Test","  carol@hermex.test  "]`), http.StatusNoContent, "set sendas")
	want := []string{"bob@hermex.test", "carol@hermex.test"}
	if store.setSendAsDir != "/mb/alice" || !slices.Equal(store.setSendAsVal, want) {
		t.Errorf("stored send-as = %q/%v, want /mb/alice/%v", store.setSendAsDir, store.setSendAsVal, want)
	}
}

// TestAdminSetUserSendAsRejectsUnknown proves a grantee that names no real user is
// refused, not stored as a grant the MTA could never honor.
func TestAdminSetUserSendAsRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store) // only alice resolves
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/sendas", session, csrf,
		`["ghost@hermex.test"]`), http.StatusNotFound, "unknown sendas grantee")
	if store.setSendAsVal != nil {
		t.Errorf("unknown grantee stored as %v, want no store call", store.setSendAsVal)
	}
}

// TestAdminUserSendAsOutsideScope proves a domain admin cannot read or write the
// send-as list of a user in another domain through the user-scoped API, and cannot
// use the panel form, which is system-only.
func TestAdminUserSendAsOutsideScope(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "hermex.test"}},
		knownUsers: map[string]directory.UserDetail{
			"alice@hermex.test": {Username: "alice@hermex.test", DomainID: 2, Maildir: "/mb/alice"},
			"bob@acme.test":     {Username: "bob@acme.test", DomainID: 1, Maildir: "/mb/bob"},
		}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	unauthResp, err := http.Get(ts.URL + "/admin/users/alice@hermex.test/sendas")
	mustNoErr(t, err, "unauth sendas request")
	wantStatus(t, unauthResp, http.StatusUnauthorized, "unauth sendas")

	// The grantee is in the caller's own domain, so only the target user's scope
	// can refuse this request.
	wantStatus(t, authedGET(t, ts, "/admin/users/alice@hermex.test/sendas", session), http.StatusForbidden, "domain admin GET sendas")
	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/sendas", session, csrf, `["bob@acme.test"]`), http.StatusForbidden, "domain admin PUT sendas")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sendas", session, csrf,
		url.Values{"sendas": {"x@y.test"}}), http.StatusForbidden, "domain-admin UI send-as")
	if store.setSendAsDir != "" {
		t.Errorf("a refused caller stored send-as on %q: %v", store.setSendAsDir, store.setSendAsVal)
	}
}

// TestUIUserDetailShowsSendAs proves the detail page renders the send-as section with
// the stored list populated.
func TestUIUserDetailShowsSendAs(t *testing.T) {
	store := &fakeStore{sendAs: map[string][]string{"/mb/alice": {"bob@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "detail page send-as")
	for _, want := range []string{"<h2>Send as</h2>", `name="sendas"`, "bob@hermex.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page send-as section missing %q", want)
		}
	}
}

// TestUIUserSendAs proves the detail-form save writes the send-as list and reports
// success.
func TestUIUserSendAs(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sendas", session, csrf, url.Values{
		"sendas": {"bob@hermex.test\n  carol@hermex.test  \n\n"},
	}), http.StatusOK, "ui sendas save")
	want := []string{"bob@hermex.test", "carol@hermex.test"}
	if !slices.Equal(store.setSendAsVal, want) {
		t.Errorf("stored send-as = %v, want %v (trimmed, blanks dropped)", store.setSendAsVal, want)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ui send-as save did not report success:\n%s", body)
	}
}

// TestUIUserSendAsRejectsUnknown proves the form refuses an unknown grantee and
// reports it rather than storing a dead grant.
func TestUIUserSendAsRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sendas", session, csrf, url.Values{
		"sendas": {"ghost@hermex.test"},
	}), http.StatusOK, "ui unknown sendas")
	if store.setSendAsVal != nil {
		t.Errorf("unknown grantee stored as %v, want no store call", store.setSendAsVal)
	}
	if !strings.Contains(body, "No such user") {
		t.Errorf("panel did not report the unknown grantee:\n%s", body)
	}
}
