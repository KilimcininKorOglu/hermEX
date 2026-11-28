package admin

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAdminGetUserStoreOwners proves a system admin reads a user's store-owner list.
func TestAdminGetUserStoreOwners(t *testing.T) {
	store := &fakeStore{storeOwners: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/users/alice@hermex.test/storeowners", session), http.StatusOK, "get storeowners")
	if !strings.Contains(body, `"data":["boss@hermex.test"]`) {
		t.Errorf("store-owners body = %s, want the owner list", body)
	}
}

// TestAdminSetUserStoreOwners proves a system admin writes the store-owner list, stored
// in the lowercased canonical form the permission check compares against.
func TestAdminSetUserStoreOwners(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store) // resolves any grantee
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/storeowners", session, csrf,
		`["BOSS@Hermex.Test","  assistant@hermex.test  "]`), http.StatusNoContent, "set storeowners")
	want := []string{"boss@hermex.test", "assistant@hermex.test"}
	if store.setStoreOwnersDir != "/mb/alice" || !slices.Equal(store.setStoreOwnersVal, want) {
		t.Errorf("stored store-owners = %q/%v, want /mb/alice/%v", store.setStoreOwnersDir, store.setStoreOwnersVal, want)
	}
}

// TestAdminSetUserStoreOwnersRejectsUnknown proves an owner that names no real user is
// refused, a privileged full-access grant must never name a non-existent principal.
func TestAdminSetUserStoreOwnersRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store) // only alice resolves
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/storeowners", session, csrf,
		`["ghost@hermex.test"]`), http.StatusNotFound, "unknown storeowner")
	if store.setStoreOwnersVal != nil {
		t.Errorf("unknown owner stored as %v, want no store call", store.setStoreOwnersVal)
	}
}

// TestAdminUserStoreOwnersRequiresSystem proves a domain admin cannot read or write the
// store-owner list of a user outside its scope through the user-scoped API, and cannot
// use the panel form, which is system-only. The owner is in the caller's own domain,
// so only the target user's scope refuses the API write.
func TestAdminUserStoreOwnersRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "hermex.test"}},
		knownUsers: map[string]directory.UserDetail{
			"alice@hermex.test": {Username: "alice@hermex.test", DomainID: 2, Maildir: "/mb/alice"},
			"bob@acme.test":     {Username: "bob@acme.test", DomainID: 1, Maildir: "/mb/bob"},
		}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	unauthResp, err := http.Get(ts.URL + "/admin/users/alice@hermex.test/storeowners")
	mustNoErr(t, err, "unauth storeowners request")
	wantStatus(t, unauthResp, http.StatusUnauthorized, "unauth storeowners")

	wantStatus(t, authedGET(t, ts, "/admin/users/alice@hermex.test/storeowners", session), http.StatusForbidden, "domain admin GET storeowners")
	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/storeowners", session, csrf, `["bob@acme.test"]`), http.StatusForbidden, "domain admin PUT storeowners")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/storeowners", session, csrf,
		url.Values{"storeowners": {"bob@acme.test"}}), http.StatusForbidden, "domain-admin UI store-owners")
	if store.setStoreOwnersDir != "" {
		t.Errorf("a refused caller stored owners on %q: %v", store.setStoreOwnersDir, store.setStoreOwnersVal)
	}
}

// TestUIUserDetailShowsStoreOwners proves the detail page renders the store-owner
// section with the stored list populated.
func TestUIUserDetailShowsStoreOwners(t *testing.T) {
	store := &fakeStore{storeOwners: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "detail page store-owners")
	for _, want := range []string{"<h2>Additional store owners</h2>", `name="storeowners"`, "boss@hermex.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page store-owner section missing %q", want)
		}
	}
}

// TestUIUserStoreOwners proves the detail-form save writes the store-owner list and
// reports success.
func TestUIUserStoreOwners(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/storeowners", session, csrf, url.Values{
		"storeowners": {"boss@hermex.test\n  assistant@hermex.test  \n\n"},
	}), http.StatusOK, "ui storeowners save")
	want := []string{"boss@hermex.test", "assistant@hermex.test"}
	if !slices.Equal(store.setStoreOwnersVal, want) {
		t.Errorf("stored store-owners = %v, want %v (trimmed, blanks dropped)", store.setStoreOwnersVal, want)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ui store-owners save did not report success:\n%s", body)
	}
}

// TestUIUserStoreOwnersRejectsUnknown proves the form refuses an unknown owner and
// reports it rather than storing a dead privileged grant.
func TestUIUserStoreOwnersRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/storeowners", session, csrf, url.Values{
		"storeowners": {"ghost@hermex.test"},
	}), http.StatusOK, "ui unknown storeowner")
	if store.setStoreOwnersVal != nil {
		t.Errorf("unknown owner stored as %v, want no store call", store.setStoreOwnersVal)
	}
	if !strings.Contains(body, "No such user") {
		t.Errorf("panel did not report the unknown owner:\n%s", body)
	}
}
