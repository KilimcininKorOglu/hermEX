package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// forwardUserDir is a system-admin directory whose one user can carry a forward.
func forwardUserDir() *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"},
	}
}

// TestAdminGetUserForward proves a system admin reads a user's forward directive.
func TestAdminGetUserForward(t *testing.T) {
	d := forwardUserDir()
	d.forward, d.forwardSet = directory.ForwardInfo{Type: directory.ForwardRedirect, Destination: "boss@hermex.test"}, true
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/forward", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get forward status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"forwardType":1`) || !strings.Contains(string(body), `"destination":"boss@hermex.test"`) {
		t.Errorf("forward body = %s, want the directive", body)
	}
}

// TestAdminSetUserForward proves a system admin writes a user's forward directive.
func TestAdminSetUserForward(t *testing.T) {
	d := forwardUserDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/forward", session, csrf,
		`{"forwardType":1,"destination":"boss@hermex.test"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set forward status %d, want 204", resp.StatusCode)
	}
	if d.setForwardUser != "alice@hermex.test" || d.setForwardType != directory.ForwardRedirect || d.setForwardDest != "boss@hermex.test" {
		t.Errorf("captured forward = %q/%d/%q, want alice/Redirect/boss", d.setForwardUser, d.setForwardType, d.setForwardDest)
	}
}

// TestAdminUserForwardRequiresSystem proves a domain admin cannot read or write a
// user's forward through the system-scoped endpoints.
func TestAdminUserForwardRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/forward", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/forward", session, csrf, `{"forwardType":0,"destination":"x@y.test"}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin forward = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsForward proves the detail page renders the forward section with
// the stored type pre-selected and the destination populated.
func TestUIUserDetailShowsForward(t *testing.T) {
	d := forwardUserDir()
	d.forward, d.forwardSet = directory.ForwardInfo{Type: directory.ForwardRedirect, Destination: "boss@hermex.test"}, true
	ts := adminServerStore(t, d, &fakeStore{})
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{">Forward<", `name="forwardtype"`, `value="redirect" selected`, `value="boss@hermex.test"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page forward section missing %q", want)
		}
	}
}

// TestUIUserForward proves the detail-form save writes the forward directive and
// reports success.
func TestUIUserForward(t *testing.T) {
	d := forwardUserDir()
	ts := adminServerStore(t, d, &fakeStore{})
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/forward", session, csrf, url.Values{
		"forwardtype": {"redirect"},
		"destination": {"boss@hermex.test"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui forward save status %d, want 200", resp.StatusCode)
	}
	if d.setForwardUser != "alice@hermex.test" || d.setForwardType != directory.ForwardRedirect || d.setForwardDest != "boss@hermex.test" {
		t.Errorf("captured forward = %q/%d/%q, want alice/Redirect/boss", d.setForwardUser, d.setForwardType, d.setForwardDest)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui forward save did not report success:\n%s", body)
	}
}

// TestUIUserForwardClears proves the "—" type clears the directive by passing an empty
// destination through to the directory.
func TestUIUserForwardClears(t *testing.T) {
	d := forwardUserDir()
	ts := adminServerStore(t, d, &fakeStore{})
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/forward", session, csrf, url.Values{
		"forwardtype": {""},
		"destination": {"boss@hermex.test"}, // ignored when the type is none
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui forward clear status %d, want 200", resp.StatusCode)
	}
	if d.setForwardDest != "" {
		t.Errorf("clear passed destination %q, want empty (clears the directive)", d.setForwardDest)
	}
}
