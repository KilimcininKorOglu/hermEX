package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// granteeScopeServer builds a panel served by a domain administrator scoped to
// acme.test only, with a user in each of two served domains.
func granteeScopeServer(t *testing.T) (ts *httptest.Server, session, csrf string) {
	t.Helper()
	d := &fakeDir{
		authOK: true, uid: 7,
		perms:   []directory.Permission{{Name: directory.PermDomainAdmin, Params: "1"}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "other.test"}},
		knownUsers: map[string]directory.UserDetail{
			"in@acme.test":   {Username: "in@acme.test", ID: 10, DomainID: 1, Maildir: t.TempDir()},
			"peer@acme.test": {Username: "peer@acme.test", ID: 11, DomainID: 1, Maildir: t.TempDir()},
			"ceo@other.test": {Username: "ceo@other.test", ID: 12, DomainID: 2, Maildir: t.TempDir()},
		},
	}
	ts = adminServer(t, d)
	session, csrf = loginCookies(t, ts)
	return ts, session, csrf
}

// TestDomainAdminGranteeScope is the cross-domain backdoor boundary. A domain
// administrator legitimately manages in@acme.test, but each of these settings
// names a SECOND, independent account, and the target's own domain says nothing
// about it. Without the check the admin could hand an account in a domain they
// have no authority over a standing grant into a mailbox they do administer. The
// store-owner grant is the sharpest: it is full read-write access to the whole
// mailbox, and webmail honours it on its own.
func TestDomainAdminGranteeScope(t *testing.T) {
	cases := []struct {
		name, path, inScope, foreign string
	}{
		{
			name:    "send-as",
			path:    "/admin/users/in@acme.test/sendas",
			inScope: `["peer@acme.test"]`,
			foreign: `["ceo@other.test"]`,
		},
		{
			name:    "send-on-behalf",
			path:    "/admin/users/in@acme.test/sendonbehalf",
			inScope: `["peer@acme.test"]`,
			foreign: `["ceo@other.test"]`,
		},
		{
			name:    "store owners",
			path:    "/admin/users/in@acme.test/storeowners",
			inScope: `["peer@acme.test"]`,
			foreign: `["ceo@other.test"]`,
		},
		{
			name:    "folder permission",
			path:    "/admin/users/in@acme.test/folders/13/permissions",
			inScope: `{"username":"peer@acme.test","rights":1024}`,
			foreign: `{"username":"ceo@other.test","rights":1024}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, session, csrf := granteeScopeServer(t)
			// Accepted, not merely some other refusal: a 400 or 500 here would make
			// the 403 below prove nothing about the scope check.
			if s := statusOf(authedReq(t, ts, "PUT", c.path, session, csrf, c.inScope)); s < 200 || s > 299 {
				t.Errorf("an own-domain %s grantee = %d, want accepted", c.name, s)
			}
			if s := statusOf(authedReq(t, ts, "PUT", c.path, session, csrf, c.foreign)); s != http.StatusForbidden {
				t.Errorf("a foreign-domain %s grantee = %d, want 403", c.name, s)
			}
		})
	}
}

// TestSystemAdminGranteeUnrestricted is the positive control: the gate contains
// domain administrators without constraining the system role, which does have
// authority over every domain.
func TestSystemAdminGranteeUnrestricted(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 1,
		// acme and other are two domains of one organization, so a grant across
		// them stays inside it; the gate under test is the admin's own scope.
		sameOrg: map[string]bool{"acme.test": true, "other.test": true},
		roles:   []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "other.test"}},
		knownUsers: map[string]directory.UserDetail{
			"in@acme.test":   {Username: "in@acme.test", ID: 10, DomainID: 1, Maildir: t.TempDir()},
			"ceo@other.test": {Username: "ceo@other.test", ID: 12, DomainID: 2, Maildir: t.TempDir()},
		},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	for _, path := range []string{
		"/admin/users/in@acme.test/sendas",
		"/admin/users/in@acme.test/storeowners",
	} {
		if s := statusOf(authedReq(t, ts, "PUT", path, session, csrf, `["ceo@other.test"]`)); s < 200 || s > 299 {
			t.Errorf("system admin cross-domain grantee on %s = %d, want accepted", path, s)
		}
	}
}

// TestGrantsStayInsideTheOwnersOrganization: a send-as, send-on-behalf,
// store-owner or delegate grant opens the mailbox or its identity to the grantee,
// which every client surface keeps inside the organization, so even a system
// administrator cannot grant one to an account of another organization, through
// the API or the detail form, and nothing is stored.
func TestGrantsStayInsideTheOwnersOrganization(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 1,
		roles:   []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "rival.test"}},
		knownUsers: map[string]directory.UserDetail{
			"in@acme.test":   {Username: "in@acme.test", ID: 10, DomainID: 1, Maildir: "/mb/in"},
			"spy@rival.test": {Username: "spy@rival.test", ID: 12, DomainID: 2, Maildir: "/mb/spy"},
		},
	}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	for _, path := range []string{"/admin/users/in@acme.test/sendas", "/admin/users/in@acme.test/sendonbehalf", "/admin/users/in@acme.test/storeowners"} {
		if s := statusOf(authedReq(t, ts, "PUT", path, session, csrf, `["spy@rival.test"]`)); s != http.StatusForbidden {
			t.Errorf("PUT %s with a grantee of another organization = %d, want 403", path, s)
		}
	}
	for field, path := range map[string]string{"delegates": "delegates", "sendas": "sendas", "storeowners": "storeowners"} {
		resp := htmxPUT(t, ts, "/admin/ui/users/in@acme.test/"+path, session, csrf, url.Values{field: {"spy@rival.test"}})
		body := wantBody(t, resp, http.StatusOK, "rival org save")
		if strings.Contains(body, "Saved") {
			t.Errorf("form %s saved a grantee of another organization", path)
		}
	}
	if store.setSendAsVal != nil || store.setSendOnBehalfVal != nil || store.setDelegatesVal != nil {
		t.Errorf("a grant to another organization was stored: send-as %v, delegates %v", store.setSendAsVal, store.setDelegatesVal)
	}
}

// TestAFailedScopeReadIsNotAnOrganizationRefusal: when the directory cannot say
// whether a grantee shares the owner's organization, the panel stores nothing,
// answers the API with a 500 that keeps the error server-side and records it, and
// shows the form the read failure rather than claiming the grantee is outside the
// organization.
func TestAFailedScopeReadIsNotAnOrganizationRefusal(t *testing.T) {
	errs := map[string]error{}
	d := &fakeDir{
		authOK: true, uid: 1,
		roles:   []directory.AdminRole{{Role: directory.AdminSystem}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}},
		knownUsers: map[string]directory.UserDetail{
			"in@acme.test":   {Username: "in@acme.test", ID: 10, DomainID: 1, Maildir: "/mb/in"},
			"peer@acme.test": {Username: "peer@acme.test", ID: 11, DomainID: 1, Maildir: "/mb/peer"},
		},
		readErrs: errs,
	}
	store := &fakeStore{}
	ts, sink := loggingAdminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)
	errs["SameScope"] = errReadFailed

	unauth := authedReq(t, ts, "PUT", "/admin/users/in@acme.test/sendas", "invalid-session", csrf, `["peer@acme.test"]`)
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated sendas PUT")

	resp := authedReq(t, ts, "PUT", "/admin/users/in@acme.test/sendas", session, csrf, `["peer@acme.test"]`)
	body := wantBody(t, resp, http.StatusInternalServerError, "generic 500")
	if strings.Contains(body, errReadFailed.Error()) {
		t.Errorf("API: status 500 body %q, want a generic 500", body)
	}
	if e, ok := sink.find("request.fail"); !ok || e.Err != errReadFailed.Error() {
		t.Errorf("the failed scope read was not recorded (event %+v)", e)
	}
	for field, path := range map[string]string{"delegates": "delegates", "sendas": "sendas", "storeowners": "storeowners"} {
		form := htmxPUT(t, ts, "/admin/ui/users/in@acme.test/"+path, session, csrf, url.Values{field: {"peer@acme.test"}})
		page := wantBody(t, form, http.StatusOK, "failed read page")
		if !strings.Contains(page, "Could not read the grantees") || strings.Contains(page, "organization") {
			t.Errorf("form %s did not report the failed read:\n%s", path, page)
		}
	}
	if store.setSendAsVal != nil || store.setDelegatesVal != nil {
		t.Errorf("a list was stored after a failed scope read: send-as %v, delegates %v", store.setSendAsVal, store.setDelegatesVal)
	}
}
