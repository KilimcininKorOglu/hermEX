package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestAdminListUsers proves a system admin lists every user.
func TestAdminListUsers(t *testing.T) {
	d := &fakeDir{
		authOK: true,
		uid:    7,
		roles:  []directory.AdminRole{{Role: directory.AdminSystem}},
		users:  []directory.UserInfo{{ID: 1, Username: "boss@hermex.test", DomainID: 1}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list users status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "boss@hermex.test") {
		t.Errorf("list body = %s, want the user", body)
	}
}

// TestAdminListUsersRequiresSystem proves a domain admin (not a system admin) is
// refused the global user list.
func TestAdminListUsersRequiresSystem(t *testing.T) {
	d := &fakeDir{
		authOK: true,
		uid:    7,
		roles:  []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin list users = %d, want 403", resp.StatusCode)
	}
}
