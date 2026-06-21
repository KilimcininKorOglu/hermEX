package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// systemAdminDir builds a fake directory whose caller is a full system admin.
func systemAdminDir() *fakeDir {
	return &fakeDir{authOK: true, uid: 1, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
}

// TestAdminNamedRolesCRUD drives the named-role JSON API end-to-end: create
// returns 201 and an id, get and list reflect the role with its permissions,
// update replaces them, and delete removes it (get afterwards is 404).
func TestAdminNamedRolesCRUD(t *testing.T) {
	ts := adminServer(t, systemAdminDir())
	session, csrf := loginCookies(t, ts)

	body := `{"name":"Helpdesk","description":"Reset passwords",` +
		`"permissions":[{"name":"ResetPasswd","params":""},{"name":"DomainAdmin","params":"*"}],` +
		`"userIDs":[5,7]}`
	resp := authedPOST(t, ts, "/admin/roles", session, csrf, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d, want 201", resp.StatusCode)
	}
	var created struct{ ID int64 }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == 0 {
		t.Fatal("create returned id 0")
	}

	get := authedGET(t, ts, "/admin/roles/"+itoa(created.ID), session)
	gotBody, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status %d, want 200", get.StatusCode)
	}
	for _, want := range []string{"Helpdesk", "ResetPasswd", "DomainAdmin"} {
		if !strings.Contains(string(gotBody), want) {
			t.Errorf("get body missing %q: %s", want, gotBody)
		}
	}

	list := authedGET(t, ts, "/admin/roles", session)
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != http.StatusOK || !strings.Contains(string(listBody), "Helpdesk") {
		t.Fatalf("list status %d body %s", list.StatusCode, listBody)
	}

	upd := authedPUT(t, ts, "/admin/roles/"+itoa(created.ID), session, csrf,
		`{"name":"Helpdesk RO","description":"","permissions":[{"name":"SystemAdminRO","params":""}],"userIDs":[]}`)
	upd.Body.Close()
	if upd.StatusCode != http.StatusNoContent {
		t.Fatalf("update status %d, want 204", upd.StatusCode)
	}
	get2 := authedGET(t, ts, "/admin/roles/"+itoa(created.ID), session)
	g2, _ := io.ReadAll(get2.Body)
	get2.Body.Close()
	if !strings.Contains(string(g2), "Helpdesk RO") || !strings.Contains(string(g2), "SystemAdminRO") {
		t.Errorf("after update body = %s", g2)
	}

	del := authedDELETE(t, ts, "/admin/roles/"+itoa(created.ID), session, csrf, "")
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d, want 204", del.StatusCode)
	}
	gone := authedGET(t, ts, "/admin/roles/"+itoa(created.ID), session)
	gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete status %d, want 404", gone.StatusCode)
	}
}

// TestAdminRolePermissionsCatalog proves the editor catalog lists every
// permission type with its scope kind.
func TestAdminRolePermissionsCatalog(t *testing.T) {
	ts := adminServer(t, systemAdminDir())
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/roles/permissions", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{
		"SystemAdmin", "SystemAdminRO", "OrgAdmin", "DomainAdmin",
		"DomainAdminRO", "DomainPurge", "ResetPasswd", `"org"`, `"domain"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("permissions catalog missing %q: %s", want, body)
		}
	}
}

// TestAdminRoleCreateMapsError proves a directory rejection (e.g. a bad name or
// permission, which the directory layer validates) surfaces as a 400. The
// validation itself is covered at the directory layer; here the fake injects the
// error to pin the handler's mapping.
func TestAdminRoleCreateMapsError(t *testing.T) {
	d := systemAdminDir()
	d.createErr = errors.New("role name must be 1..64 characters")
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := authedPOST(t, ts, "/admin/roles", session, csrf, `{"name":""}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("create with directory error: status %d, want 400", resp.StatusCode)
	}
}

// TestAdminRoleNotFound proves get/update/delete on an unknown id are 404.
func TestAdminRoleNotFound(t *testing.T) {
	ts := adminServer(t, systemAdminDir())
	session, csrf := loginCookies(t, ts)
	cases := []struct {
		name string
		do   func() *http.Response
	}{
		{"get", func() *http.Response { return authedGET(t, ts, "/admin/roles/999", session) }},
		{"update", func() *http.Response { return authedPUT(t, ts, "/admin/roles/999", session, csrf, `{"name":"X"}`) }},
		{"delete", func() *http.Response { return authedDELETE(t, ts, "/admin/roles/999", session, csrf, "") }},
	}
	for _, c := range cases {
		resp := c.do()
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s unknown role: status %d, want 404", c.name, resp.StatusCode)
		}
	}
}
