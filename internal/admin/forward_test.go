package admin

import (
	"encoding/json"
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

	body := wantBody(t, authedGET(t, ts, "/admin/users/alice@hermex.test/forward", session), http.StatusOK, "get forward")
	if !strings.Contains(body, `"forwardType":1`) || !strings.Contains(body, `"destination":"boss@hermex.test"`) {
		t.Errorf("forward body = %s, want the directive", body)
	}
}

// TestAdminSetUserForward proves a system admin writes a user's forward directive.
func TestAdminSetUserForward(t *testing.T) {
	d := forwardUserDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/forward", session, csrf,
		`{"forwardType":1,"destination":"boss@hermex.test"}`), http.StatusNoContent, "set forward")
	if d.setForwardUser != "alice@hermex.test" || d.setForwardType != directory.ForwardRedirect || d.setForwardDest != "boss@hermex.test" {
		t.Errorf("captured forward = %q/%d/%q, want alice/Redirect/boss", d.setForwardUser, d.setForwardType, d.setForwardDest)
	}
}

// TestAdminUserForwardRequiresSystem proves a domain admin cannot read or write the
// forward of a user outside its scope through the user-scoped API, and cannot use
// the panel form, which is system-only.
func TestAdminUserForwardRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"}}
	ts := adminServerStore(t, d, &fakeStore{})
	session, csrf := loginCookies(t, ts)

	unauthResp, err := http.Get(ts.URL + "/admin/users/alice@hermex.test/forward")
	mustNoErr(t, err, "unauth forward request")
	wantStatus(t, unauthResp, http.StatusUnauthorized, "unauth forward")

	wantStatus(t, authedGET(t, ts, "/admin/users/alice@hermex.test/forward", session), http.StatusForbidden, "domain admin GET forward")
	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/forward", session, csrf, `{"forwardType":0,"destination":"x@y.test"}`), http.StatusForbidden, "domain admin PUT forward")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/forward", session, csrf,
		url.Values{"forwardtype": {"redirect"}, "destination": {"x@y.test"}}), http.StatusForbidden, "domain-admin UI forward")
	if d.setForwardUser != "" {
		t.Errorf("a domain admin set a forward on %q to %q", d.setForwardUser, d.setForwardDest)
	}
}

// TestUIUserDetailShowsForward proves the detail page renders the forward section with
// the stored type pre-selected and the destination populated.
func TestUIUserDetailShowsForward(t *testing.T) {
	d := forwardUserDir()
	d.forward, d.forwardSet = directory.ForwardInfo{Type: directory.ForwardRedirect, Destination: "boss@hermex.test"}, true
	ts := adminServerStore(t, d, &fakeStore{})
	session, _ := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "detail page forward")
	for _, want := range []string{">Forward<", `name="forwardtype"`, `value="redirect" selected`, `value="boss@hermex.test"`} {
		if !strings.Contains(body, want) {
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

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/forward", session, csrf, url.Values{
		"forwardtype": {"redirect"},
		"destination": {"boss@hermex.test"},
	}), http.StatusOK, "ui forward save")
	if d.setForwardUser != "alice@hermex.test" || d.setForwardType != directory.ForwardRedirect || d.setForwardDest != "boss@hermex.test" {
		t.Errorf("captured forward = %q/%d/%q, want alice/Redirect/boss", d.setForwardUser, d.setForwardType, d.setForwardDest)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ui forward save did not report success:\n%s", body)
	}
}

// TestUserForwardRefusesWhatIsNotOneAddress sends destinations that delivery cannot
// route: no @, a list, a display name, a CRLF-injected header and a domain that is
// not a host name. Under a Redirect each would bounce every message the account
// receives, so the form and the JSON API refuse them and the directory is never
// asked to store one.
func TestUserForwardRefusesWhatIsNotOneAddress(t *testing.T) {
	bad := []string{"boss", "a@x.test, b@x.test", "Boss <boss@x.test>", "boss@x.test\r\nBcc: e@evil.test", "boss@x_y.test"}
	for _, dest := range bad {
		d := forwardUserDir()
		ts := adminServerStore(t, d, &fakeStore{})
		session, csrf := loginCookies(t, ts)

		resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/forward", session, csrf,
			url.Values{"forwardtype": {"redirect"}, "destination": {dest}})
		body := wantBody(t, resp, http.StatusOK, "form "+dest)
		wantContains(t, body, "single email address", "the form names the refusal for "+dest)
		payload, err := json.Marshal(map[string]any{"forwardType": directory.ForwardRedirect, "destination": dest})
		mustNoErr(t, err, "marshal forward JSON")
		wantStatus(t, authedReq(t, ts, "PUT", "/admin/users/alice@hermex.test/forward", session, csrf, string(payload)),
			http.StatusBadRequest, "JSON "+dest)
		if d.setForwardDest != "" {
			t.Errorf("%q reached the directory as %q", dest, d.setForwardDest)
		}
	}
}

// TestUIUserForwardClears proves the empty type clears the directive by passing an empty
// destination through to the directory.
func TestUIUserForwardClears(t *testing.T) {
	d := forwardUserDir()
	ts := adminServerStore(t, d, &fakeStore{})
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/forward", session, csrf, url.Values{
		"forwardtype": {""},
		"destination": {"boss@hermex.test"}, // ignored when the type is none
	}), http.StatusOK, "ui forward clear")
	// The directory must be asked to clear: an empty destination alone also reads
	// the same when no write happened at all.
	if d.setForwardUser != "alice@hermex.test" {
		t.Errorf("clear wrote for %q, want the directory asked to clear alice's forward", d.setForwardUser)
	}
	if d.setForwardDest != "" {
		t.Errorf("clear passed destination %q, want empty (clears the directive)", d.setForwardDest)
	}
}
