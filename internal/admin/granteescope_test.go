package admin

import (
	"net/http"
	"net/http/httptest"
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
			if s := statusOf(authedReq(t, ts, "PUT", c.path, session, csrf, c.inScope)); s == http.StatusForbidden {
				t.Errorf("an own-domain %s grantee was refused (403)", c.name)
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
		if s := statusOf(authedReq(t, ts, "PUT", path, session, csrf, `["ceo@other.test"]`)); s == http.StatusForbidden {
			t.Errorf("system admin refused a cross-domain grantee on %s", path)
		}
	}
}
