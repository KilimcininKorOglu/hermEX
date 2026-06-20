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

// TestAdminCreateUser proves a system admin provisions a user whose maildir is
// derived through the Paths deriver.
func TestAdminCreateUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"new@hermex.test","password":"pw"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user status %d, want 201", resp.StatusCode)
	}
	if d.createdUser != "new@hermex.test" {
		t.Errorf("created user %q, want new@hermex.test", d.createdUser)
	}
	if !strings.HasSuffix(d.createdMaildir, "/mbox/new@hermex.test") {
		t.Errorf("maildir %q not derived through Paths", d.createdMaildir)
	}
}

// TestAdminCreateUserValidates proves a request missing the password is refused
// before the directory is touched.
func TestAdminCreateUserValidates(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"new@hermex.test"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing-password status %d, want 400", resp.StatusCode)
	}
	if d.createdUser != "" {
		t.Errorf("an invalid request still provisioned %q", d.createdUser)
	}
}

// TestAdminSetPassword proves a system admin resets a user's password.
func TestAdminSetPassword(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/boss@hermex.test/password", session, csrf, `{"password":"newpass"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set password status %d, want 204", resp.StatusCode)
	}
	if d.setPwUser != "boss@hermex.test" || d.setPwValue != "newpass" {
		t.Errorf("set password for %q = %q, want boss@hermex.test = newpass", d.setPwUser, d.setPwValue)
	}
}

// TestAdminSetPasswordNotFound proves resetting an unknown user's password is a
// 404.
func TestAdminSetPasswordNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:        []directory.AdminRole{{Role: directory.AdminSystem}},
		setPwMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/ghost@hermex.test/password", session, csrf, `{"password":"x"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("set password for unknown user = %d, want 404", resp.StatusCode)
	}
}

// TestAdminGetUser proves a system admin reads a single user's detail record.
func TestAdminGetUser(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:      []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{ID: 5, Username: "alice@hermex.test", DomainID: 1, Status: 1, Lang: "de", DisplayType: 7, POP3IMAP: true, LDAP: true},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get user status %d, want 200", resp.StatusCode)
	}
	if d.gotUser != "alice@hermex.test" {
		t.Errorf("GetUser called for %q, want alice@hermex.test", d.gotUser)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "alice@hermex.test") || !strings.Contains(string(body), `"Lang":"de"`) {
		t.Errorf("get user body = %s, want the detail record", body)
	}
}

// TestAdminGetUserNotFound proves reading an unknown user is a 404.
func TestAdminGetUserNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:          []directory.AdminRole{{Role: directory.AdminSystem}},
		getUserMissing: true,
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/ghost@hermex.test", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get unknown user = %d, want 404", resp.StatusCode)
	}
}

// TestAdminUpdateUser proves a system admin edits a user's account fields and the
// whole editable subset reaches the directory.
func TestAdminUpdateUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := `{"status":1,"lang":"de","timezone":"Europe/Berlin","displayType":7,"homeserver":2,"pop3_imap":true,"smtp":false}`
	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test", session, csrf, body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update user status %d, want 204", resp.StatusCode)
	}
	if d.updatedUser != "alice@hermex.test" {
		t.Errorf("UpdateUser called for %q, want alice@hermex.test", d.updatedUser)
	}
	if d.updateUser.Status != 1 || d.updateUser.Lang != "de" || d.updateUser.DisplayType != 7 ||
		d.updateUser.Homeserver != 2 || !d.updateUser.POP3IMAP || d.updateUser.SMTP {
		t.Errorf("update payload = %+v, want the submitted fields", d.updateUser)
	}
}

// TestAdminUpdateUserNotFound proves editing an unknown user is a 404.
func TestAdminUpdateUserNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:         []directory.AdminRole{{Role: directory.AdminSystem}},
		updateMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/ghost@hermex.test", session, csrf, `{"status":0}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("update unknown user = %d, want 404", resp.StatusCode)
	}
}

// TestAdminDeleteUser proves a system admin deletes a user and that the
// deleteFiles intent is carried to the directory.
func TestAdminDeleteUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/alice@hermex.test?deleteFiles=true", session, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete user status %d, want 204", resp.StatusCode)
	}
	if d.deletedUser != "alice@hermex.test" {
		t.Errorf("DeleteUser called for %q, want alice@hermex.test", d.deletedUser)
	}
	if !d.deleteFiles {
		t.Error("deleteFiles=true was not carried to the directory")
	}
}

// TestAdminDeleteUserKeepsFilesByDefault proves the maildir is preserved unless
// deleteFiles is explicitly requested — a missing flag must never destroy mail.
func TestAdminDeleteUserKeepsFilesByDefault(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/alice@hermex.test", session, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete user status %d, want 204", resp.StatusCode)
	}
	if d.deleteFiles {
		t.Error("deleteFiles defaulted to true; the maildir would be destroyed without opt-in")
	}
}

// TestAdminDeleteUserNotFound proves deleting an unknown user is a 404.
func TestAdminDeleteUserNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:         []directory.AdminRole{{Role: directory.AdminSystem}},
		deleteMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/ghost@hermex.test", session, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete unknown user = %d, want 404", resp.StatusCode)
	}
}

// TestAdminUserDetailRequiresSystem proves a domain admin cannot read, edit, or
// delete users through the system-scoped detail endpoints.
func TestAdminUserDetailRequiresSystem(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test", session, csrf, `{"status":0}`)
	put.Body.Close()
	del := authedDELETE(t, ts, "/admin/users/alice@hermex.test", session, csrf, "")
	del.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden || del.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin detail access = GET %d / PUT %d / DELETE %d, want all 403",
			get.StatusCode, put.StatusCode, del.StatusCode)
	}
}

// TestAdminListAltnames proves a system admin reads a user's alternative names.
func TestAdminListAltnames(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		altnames: []string{"ali", "alice2"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/altnames", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list altnames status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ali") || !strings.Contains(string(body), "alice2") {
		t.Errorf("altnames body = %s, want the names", body)
	}
}

// TestAdminSetAltnames proves a system admin replaces a user's alternative names
// and the submitted set reaches the directory.
func TestAdminSetAltnames(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/altnames", session, csrf, `{"altnames":["ali","alice2"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set altnames status %d, want 204", resp.StatusCode)
	}
	if d.setAltnamesUser != "alice@hermex.test" || len(d.setAltnames) != 2 || d.setAltnames[0] != "ali" {
		t.Errorf("SetAltnames = (%q, %v), want alice@hermex.test [ali alice2]", d.setAltnamesUser, d.setAltnames)
	}
}

// TestAdminSetAltnamesNotFound proves setting names for an unknown user is a 404.
func TestAdminSetAltnamesNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		altnamesMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/ghost@hermex.test/altnames", session, csrf, `{"altnames":["x"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("set altnames for unknown user = %d, want 404", resp.StatusCode)
	}
}

// TestAdminListUserAliases proves a system admin reads a user's e-mail aliases.
func TestAdminListUserAliases(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userAliases: []string{"info@hermex.test", "sales@hermex.test"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/aliases", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list user aliases status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "sales@hermex.test") {
		t.Errorf("aliases body = %s, want the aliases", body)
	}
}

// TestAdminSetUserAliases proves a system admin replaces a user's e-mail aliases
// and the submitted set reaches the directory.
func TestAdminSetUserAliases(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/aliases", session, csrf,
		`{"aliases":["info@hermex.test","sales@hermex.test"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set user aliases status %d, want 204", resp.StatusCode)
	}
	if d.setAliasesUser != "alice@hermex.test" || len(d.setAliases) != 2 {
		t.Errorf("SetAliasesFor = (%q, %v), want alice@hermex.test with 2 aliases", d.setAliasesUser, d.setAliases)
	}
}

// TestAdminSetUserAliasesNotFound proves setting aliases for an unknown user is a
// 404.
func TestAdminSetUserAliasesNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		aliasesMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/ghost@hermex.test/aliases", session, csrf, `{"aliases":["x@hermex.test"]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("set aliases for unknown user = %d, want 404", resp.StatusCode)
	}
}

// TestAdminGetContact proves a system admin reads a user's contact fields mapped
// from proptags back to field names.
func TestAdminGetContact(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userProps: map[uint32]string{0x3001001F: "Alice Liddell", 0x3A4F001F: "Ali"},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/contact", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get contact status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"displayname":"Alice Liddell"`) || !strings.Contains(string(body), `"nickname":"Ali"`) {
		t.Errorf("contact body = %s, want the named fields", body)
	}
}

// TestAdminSetContact proves a system admin writes contact fields mapped to their
// proptags, and that an unknown field name is dropped rather than stored.
func TestAdminSetContact(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/contact", session, csrf,
		`{"displayname":"Alice Liddell","title":"Curiouser","unknownfield":"ignored"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set contact status %d, want 204", resp.StatusCode)
	}
	if d.setPropsUser != "alice@hermex.test" {
		t.Errorf("SetUserProperties called for %q, want alice@hermex.test", d.setPropsUser)
	}
	if d.setProps[0x3001001F] != "Alice Liddell" || d.setProps[0x3A17001F] != "Curiouser" {
		t.Errorf("set props = %v, want displayname+title mapped to proptags", d.setProps)
	}
	if len(d.setProps) != 2 {
		t.Errorf("set props has %d entries, want 2 — an unknown field name must not become a property", len(d.setProps))
	}
}

// TestAdminSetContactNotFound proves writing contact for an unknown user is a 404.
func TestAdminSetContactNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		setPropsMissing: true,
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/ghost@hermex.test/contact", session, csrf, `{"displayname":"x"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("set contact for unknown user = %d, want 404", resp.StatusCode)
	}
}
