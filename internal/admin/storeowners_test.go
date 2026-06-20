package admin

import (
	"io"
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

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/storeowners", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get store-owners status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"data":["boss@hermex.test"]`) {
		t.Errorf("store-owners body = %s, want the owner list", body)
	}
}

// TestAdminSetUserStoreOwners proves a system admin writes the store-owner list, stored
// in the lowercased canonical form the permission check compares against.
func TestAdminSetUserStoreOwners(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store) // resolves any grantee
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/storeowners", session, csrf,
		`["BOSS@Hermex.Test","  assistant@hermex.test  "]`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set store-owners status %d, want 204", resp.StatusCode)
	}
	want := []string{"boss@hermex.test", "assistant@hermex.test"}
	if store.setStoreOwnersDir != "/mb/alice" || !slices.Equal(store.setStoreOwnersVal, want) {
		t.Errorf("stored store-owners = %q/%v, want /mb/alice/%v", store.setStoreOwnersDir, store.setStoreOwnersVal, want)
	}
}

// TestAdminSetUserStoreOwnersRejectsUnknown proves an owner that names no real user is
// refused — a privileged full-access grant must never name a non-existent principal.
func TestAdminSetUserStoreOwnersRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store) // only alice resolves
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/storeowners", session, csrf,
		`["ghost@hermex.test"]`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-owner status %d, want 404", resp.StatusCode)
	}
	if store.setStoreOwnersVal != nil {
		t.Errorf("unknown owner stored as %v, want no store call", store.setStoreOwnersVal)
	}
}

// TestAdminUserStoreOwnersRequiresSystem proves a domain admin cannot read or write a
// user's store-owner list through the system-scoped endpoints — granting full mailbox
// access is a system-administrator privilege.
func TestAdminUserStoreOwnersRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/storeowners", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/storeowners", session, csrf, `["x@y.test"]`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin store-owners = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsStoreOwners proves the detail page renders the store-owner
// section with the stored list populated.
func TestUIUserDetailShowsStoreOwners(t *testing.T) {
	store := &fakeStore{storeOwners: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"<h2>Additional store owners</h2>", `name="storeowners"`, "boss@hermex.test"} {
		if !strings.Contains(string(body), want) {
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

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/storeowners", session, csrf, url.Values{
		"storeowners": {"boss@hermex.test\n  assistant@hermex.test  \n\n"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui store-owners save status %d, want 200", resp.StatusCode)
	}
	want := []string{"boss@hermex.test", "assistant@hermex.test"}
	if !slices.Equal(store.setStoreOwnersVal, want) {
		t.Errorf("stored store-owners = %v, want %v (trimmed, blanks dropped)", store.setStoreOwnersVal, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui store-owners save did not report success:\n%s", body)
	}
}

// TestUIUserStoreOwnersRejectsUnknown proves the form refuses an unknown owner and
// reports it rather than storing a dead privileged grant.
func TestUIUserStoreOwnersRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/storeowners", session, csrf, url.Values{
		"storeowners": {"ghost@hermex.test"},
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if store.setStoreOwnersVal != nil {
		t.Errorf("unknown owner stored as %v, want no store call", store.setStoreOwnersVal)
	}
	if !strings.Contains(string(body), "No such user") {
		t.Errorf("panel did not report the unknown owner:\n%s", body)
	}
}
