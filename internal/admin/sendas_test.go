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

// TestAdminGetUserSendAs proves a system admin reads a user's send-as list.
func TestAdminGetUserSendAs(t *testing.T) {
	store := &fakeStore{sendAs: map[string][]string{"/mb/alice": {"bob@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/sendas", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get send-as status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"data":["bob@hermex.test"]`) {
		t.Errorf("send-as body = %s, want the grantee list", body)
	}
}

// TestAdminSetUserSendAs proves a system admin writes the send-as list, stored in the
// lowercased canonical form the MTA compares against.
func TestAdminSetUserSendAs(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store) // resolves any grantee
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/sendas", session, csrf,
		`["BOB@Hermex.Test","  carol@hermex.test  "]`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set send-as status %d, want 204", resp.StatusCode)
	}
	want := []string{"bob@hermex.test", "carol@hermex.test"}
	if store.setSendAsDir != "/mb/alice" || !slices.Equal(store.setSendAsVal, want) {
		t.Errorf("stored send-as = %q/%v, want /mb/alice/%v", store.setSendAsDir, store.setSendAsVal, want)
	}
}

// TestAdminSetUserSendAsRejectsUnknown proves a grantee that names no real user is
// refused — not stored as a grant the MTA could never honor.
func TestAdminSetUserSendAsRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store) // only alice resolves
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/sendas", session, csrf,
		`["ghost@hermex.test"]`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-grantee status %d, want 404", resp.StatusCode)
	}
	if store.setSendAsVal != nil {
		t.Errorf("unknown grantee stored as %v, want no store call", store.setSendAsVal)
	}
}

// TestAdminUserSendAsRequiresSystem proves a domain admin cannot read or write a
// user's send-as list through the system-scoped endpoints.
func TestAdminUserSendAsRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/sendas", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/sendas", session, csrf, `["x@y.test"]`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin send-as = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsSendAs proves the detail page renders the send-as section with
// the stored list populated.
func TestUIUserDetailShowsSendAs(t *testing.T) {
	store := &fakeStore{sendAs: map[string][]string{"/mb/alice": {"bob@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"<h2>Send as</h2>", `name="sendas"`, "bob@hermex.test"} {
		if !strings.Contains(string(body), want) {
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

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sendas", session, csrf, url.Values{
		"sendas": {"bob@hermex.test\n  carol@hermex.test  \n\n"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui send-as save status %d, want 200", resp.StatusCode)
	}
	want := []string{"bob@hermex.test", "carol@hermex.test"}
	if !slices.Equal(store.setSendAsVal, want) {
		t.Errorf("stored send-as = %v, want %v (trimmed, blanks dropped)", store.setSendAsVal, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui send-as save did not report success:\n%s", body)
	}
}

// TestUIUserSendAsRejectsUnknown proves the form refuses an unknown grantee and
// reports it rather than storing a dead grant.
func TestUIUserSendAsRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sendas", session, csrf, url.Values{
		"sendas": {"ghost@hermex.test"},
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if store.setSendAsVal != nil {
		t.Errorf("unknown grantee stored as %v, want no store call", store.setSendAsVal)
	}
	if !strings.Contains(string(body), "No such user") {
		t.Errorf("panel did not report the unknown grantee:\n%s", body)
	}
}
