package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUIRolesPage proves the roles list page renders for an admin.
func TestUIRolesPage(t *testing.T) {
	d := systemAdminDir()
	d.namedRoles = map[int64]directory.RoleDetail{
		1: roleDetail(1, "Helpdesk", "Reset passwords", []directory.Permission{{Name: directory.PermResetPasswd}}, []int64{5}),
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/roles", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Helpdesk") {
		t.Errorf("roles page missing the role: %s", body)
	}
}

// TestUIRoleCreate proves the list-page form creates a role and refreshes the panel.
func TestUIRoleCreate(t *testing.T) {
	d := systemAdminDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/roles", session, csrf, url.Values{"name": {"Helpdesk"}, "description": {"d"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status %d, want 200", resp.StatusCode)
	}
	if len(d.namedRoles) != 1 {
		t.Fatalf("role not stored: %v", d.namedRoles)
	}
	if !strings.Contains(string(body), "Helpdesk") {
		t.Errorf("refreshed panel missing the new role: %s", body)
	}
}

// TestUIRoleDetailEditor proves the editor renders the current permission set
// with the assigned permissions pre-checked.
func TestUIRoleDetailEditor(t *testing.T) {
	d := systemAdminDir()
	d.users = []directory.UserInfo{{ID: 5, Username: "u@hermex.test"}}
	d.namedRoles = map[int64]directory.RoleDetail{
		1: roleDetail(1, "Helpdesk", "", []directory.Permission{
			{Name: directory.PermResetPasswd},
			{Name: directory.PermOrgAdmin, Params: "*"},
		}, []int64{5}),
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/roles/1", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	s := string(body)
	if !strings.Contains(s, `value="ResetPasswd" checked`) {
		t.Errorf("ResetPasswd capability not pre-checked: %s", s)
	}
	if !strings.Contains(s, `name="orgadmin" value="*" checked`) {
		t.Errorf("OrgAdmin(all) not pre-checked: %s", s)
	}
	if !strings.Contains(s, `value="5" selected`) {
		t.Errorf("assigned user not pre-selected: %s", s)
	}
}

// TestUIRoleSave proves the editor form rebuilds the permission set and user
// assignments and replaces the role.
func TestUIRoleSave(t *testing.T) {
	d := systemAdminDir()
	d.users = []directory.UserInfo{{ID: 5, Username: "u@hermex.test"}}
	d.namedRoles = map[int64]directory.RoleDetail{
		1: roleDetail(1, "Helpdesk", "", nil, nil),
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	form := url.Values{
		"name":        {"Helpdesk"},
		"description": {"reset only"},
		"cap":         {directory.PermResetPasswd},
		"orgadmin":    {"*"},
		"user":        {"5"},
	}
	resp := htmxPUT(t, ts, "/admin/ui/roles/1", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("save did not confirm: %s", body)
	}
	got := d.namedRoles[1]
	if !hasPerm(got.Permissions, directory.PermResetPasswd, "") ||
		!hasPerm(got.Permissions, directory.PermOrgAdmin, "*") {
		t.Errorf("saved permissions = %+v", got.Permissions)
	}
	if len(got.UserIDs) != 1 || got.UserIDs[0] != 5 {
		t.Errorf("saved users = %+v, want [5]", got.UserIDs)
	}
}

// TestUIRoleDelete proves the delete action removes the role and redirects.
func TestUIRoleDelete(t *testing.T) {
	d := systemAdminDir()
	d.namedRoles = map[int64]directory.RoleDetail{1: roleDetail(1, "Helpdesk", "", nil, nil)}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/roles/1/delete", session, csrf, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("HX-Redirect") != "/admin/ui/roles" {
		t.Errorf("delete redirect = %q, want /admin/ui/roles", resp.Header.Get("HX-Redirect"))
	}
	if len(d.namedRoles) != 0 {
		t.Errorf("role not deleted: %v", d.namedRoles)
	}
}

// TestUIRolesReadOnlyViewButNotEdit proves a read-only admin may view the roles
// UI but cannot change it: the page renders, the save is refused.
func TestUIRolesReadOnlyViewButNotEdit(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 1, perms: []directory.Permission{{Name: directory.PermSystemAdminRO}}}
	d.namedRoles = map[int64]directory.RoleDetail{1: roleDetail(1, "Helpdesk", "", nil, nil)}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	page := authedGET(t, ts, "/admin/ui/roles", session)
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Errorf("read-only admin: roles page status %d, want 200", page.StatusCode)
	}
	save := htmxPUT(t, ts, "/admin/ui/roles/1", session, csrf, url.Values{"name": {"X"}})
	save.Body.Close()
	if save.StatusCode != http.StatusForbidden {
		t.Errorf("read-only admin: role save status %d, want 403", save.StatusCode)
	}
}
