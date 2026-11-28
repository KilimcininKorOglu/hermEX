package admin

import (
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

	unauth := authedGET(t, ts, "/admin/users", "bad-session")
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated list users")

	resp := authedGET(t, ts, "/admin/users", session)
	body := wantBody(t, resp, http.StatusOK, "list users")
	if !strings.Contains(body, "boss@hermex.test") {
		t.Errorf("list body = %s, want the user", body)
	}
}

// TestAdminListUsersScopeFiltered proves the user list is scope-filtered: a
// domain admin sees only its own domain's users, never another domain's. The
// filter must not leak out-of-scope rows.
func TestAdminListUsersScopeFiltered(t *testing.T) {
	d := &fakeDir{
		authOK: true,
		uid:    7,
		roles:  []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		users: []directory.UserInfo{
			{ID: 10, Username: "a@acme.test", DomainID: 1},
			{ID: 11, Username: "b@other.test", DomainID: 2},
		},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users", session)
	body := wantBody(t, resp, http.StatusOK, "domain-admin list users")
	if !strings.Contains(body, "a@acme.test") || strings.Contains(body, "b@other.test") {
		t.Errorf("domain admin saw out-of-scope users: %s", body)
	}
}

// TestAdminCreateUser proves a system admin provisions a user whose maildir is
// derived through the Paths deriver.
func TestAdminCreateUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"new@hermex.test","password":"pw"}`)
	wantStatus(t, resp, http.StatusCreated, "create user")
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
	wantStatus(t, resp, http.StatusBadRequest, "missing-password")
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
	wantStatus(t, resp, http.StatusNoContent, "set password")
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
	wantStatus(t, resp, http.StatusNotFound, "set password for unknown user")
}

// TestAdminChangeOwnPassword proves a logged-in admin changes their OWN password
// after re-authenticating with the current one. The target is taken from the
// session (admin@hermex.test, the login()), never the request body, so a caller
// can only ever change their own account; the test pins that the update landed on
// the session login with the new value.
func TestAdminChangeOwnPassword(t *testing.T) {
	d := &fakeDir{authOK: true, password: "pw", uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/passwd", session, csrf, `{"old":"pw","new":"newsecret"}`)
	wantStatus(t, resp, http.StatusNoContent, "change own password")
	if d.setPwUser != "admin@hermex.test" || d.setPwValue != "newsecret" {
		t.Errorf("changed password for %q = %q, want admin@hermex.test = newsecret", d.setPwUser, d.setPwValue)
	}
}

// TestAdminChangeOwnPasswordWrongCurrent proves a wrong current password is a 403
// and never touches the stored password, the re-authentication is load-bearing,
// not cosmetic.
func TestAdminChangeOwnPasswordWrongCurrent(t *testing.T) {
	d := &fakeDir{authOK: true, password: "pw", uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/passwd", session, csrf, `{"old":"wrong","new":"newsecret"}`)
	wantStatus(t, resp, http.StatusForbidden, "wrong current password")
	if d.setPwValue != "" {
		t.Errorf("password was changed to %q despite a wrong current password", d.setPwValue)
	}
}

// TestAdminChangeOwnPasswordRequiresBoth proves an empty new password is a 400,
// matching the reference's required old+new fields.
func TestAdminChangeOwnPasswordRequiresBoth(t *testing.T) {
	d := &fakeDir{authOK: true, password: "pw", uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/passwd", session, csrf, `{"old":"pw","new":""}`)
	wantStatus(t, resp, http.StatusBadRequest, "empty new password")
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
	body := wantBody(t, resp, http.StatusOK, "get user")
	if d.gotUser != "alice@hermex.test" {
		t.Errorf("GetUser called for %q, want alice@hermex.test", d.gotUser)
	}
	if !strings.Contains(body, "alice@hermex.test") || !strings.Contains(body, `"Lang":"de"`) {
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
	wantStatus(t, resp, http.StatusNotFound, "get unknown user")
}

// TestAdminUpdateUser proves a system admin edits a user's account fields and the
// whole editable subset reaches the directory.
func TestAdminUpdateUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := `{"status":1,"lang":"de","timezone":"Europe/Berlin","displayType":7,"homeserver":2,"pop3_imap":true,"smtp":false}`
	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test", session, csrf, body)
	wantStatus(t, resp, http.StatusNoContent, "update user")
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
	wantStatus(t, resp, http.StatusNotFound, "update unknown user")
}

// TestAdminDeleteUser proves a system admin deletes a user and that the
// deleteFiles intent is carried to the directory.
func TestAdminDeleteUser(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/alice@hermex.test?deleteFiles=true", session, csrf, "")
	wantStatus(t, resp, http.StatusNoContent, "delete user with deleteFiles")
	if d.deletedUser != "alice@hermex.test" {
		t.Errorf("DeleteUser called for %q, want alice@hermex.test", d.deletedUser)
	}
	if !d.deleteFiles {
		t.Error("deleteFiles=true was not carried to the directory")
	}
}

// TestAdminDeleteUserKeepsFilesByDefault proves the maildir is preserved unless
// deleteFiles is explicitly requested, a missing flag must never destroy mail.
func TestAdminDeleteUserKeepsFilesByDefault(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedDELETE(t, ts, "/admin/users/alice@hermex.test", session, csrf, "")
	wantStatus(t, resp, http.StatusNoContent, "delete user default")
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
	wantStatus(t, resp, http.StatusNotFound, "delete unknown user")
}

// TestAdminUserDetailOutsideScope proves a domain admin cannot read, edit, delete,
// reset the password of, or create a user in another domain through the
// user-scoped API, while a user in its own domain stays readable, so the refusal
// comes from the scope check and not from a blanket denial.
func TestAdminUserDetailOutsideScope(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7,
		roles:   []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		domains: []directory.DomainInfo{{ID: 1, Name: "acme.test"}, {ID: 2, Name: "hermex.test"}},
		knownUsers: map[string]directory.UserDetail{
			"alice@hermex.test": {Username: "alice@hermex.test", DomainID: 2},
			"bob@acme.test":     {Username: "bob@acme.test", DomainID: 1},
		},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedGET(t, ts, "/admin/users/bob@acme.test", session), http.StatusOK, "own-domain read")
	const user = "/admin/users/alice@hermex.test"
	wantStatus(t, authedGET(t, ts, user, session), http.StatusForbidden, "GET")
	wantStatus(t, authedPUT(t, ts, user, session, csrf, `{"status":0}`), http.StatusForbidden, "PUT")
	wantStatus(t, authedDELETE(t, ts, user, session, csrf, ""), http.StatusForbidden, "DELETE")
	wantStatus(t, authedPUT(t, ts, user+"/altnames", session, csrf, `{"altnames":["ceo"]}`), http.StatusForbidden, "altnames")
	wantStatus(t, authedPUT(t, ts, user+"/aliases", session, csrf, `{"aliases":["ceo@acme.test"]}`), http.StatusForbidden, "aliases")
	wantStatus(t, authedPUT(t, ts, user+"/contact", session, csrf, `{}`), http.StatusForbidden, "contact")
	wantStatus(t, authedPOST(t, ts, user+"/password", session, csrf, `{"password":"taken"}`), http.StatusForbidden, "password")
	wantStatus(t, authedPOST(t, ts, "/admin/users", session, csrf, `{"email":"new@hermex.test","password":"pw"}`),
		http.StatusForbidden, "create in another domain")

	if d.updatedUser != "" || d.deletedUser != "" || d.setAltnamesUser != "" || d.setAliasesUser != "" ||
		d.setPropsUser != "" || d.setPwUser != "" || d.createdUser != "" {
		t.Errorf("a refused call reached the directory: update %q delete %q altnames %q aliases %q props %q password %q create %q",
			d.updatedUser, d.deletedUser, d.setAltnamesUser, d.setAliasesUser, d.setPropsUser, d.setPwUser, d.createdUser)
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
	body := wantBody(t, resp, http.StatusOK, "list altnames")
	if !strings.Contains(body, "ali") || !strings.Contains(body, "alice2") {
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
	wantStatus(t, resp, http.StatusNoContent, "set altnames")
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
	wantStatus(t, resp, http.StatusNotFound, "set altnames for unknown user")
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
	body := wantBody(t, resp, http.StatusOK, "list user aliases")
	if !strings.Contains(body, "sales@hermex.test") {
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
	wantStatus(t, resp, http.StatusNoContent, "set user aliases")
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
	wantStatus(t, resp, http.StatusNotFound, "set aliases for unknown user")
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
	body := wantBody(t, resp, http.StatusOK, "get contact")
	if !strings.Contains(body, `"displayname":"Alice Liddell"`) || !strings.Contains(body, `"nickname":"Ali"`) {
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
	wantStatus(t, resp, http.StatusNoContent, "set contact")
	if d.setPropsUser != "alice@hermex.test" {
		t.Errorf("SetUserProperties called for %q, want alice@hermex.test", d.setPropsUser)
	}
	if d.setProps[0x3001001F] != "Alice Liddell" || d.setProps[0x3A17001F] != "Curiouser" {
		t.Errorf("set props = %v, want displayname+title mapped to proptags", d.setProps)
	}
	if len(d.setProps) != 2 {
		t.Errorf("set props has %d entries, want 2, an unknown field name must not become a property", len(d.setProps))
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
	wantStatus(t, resp, http.StatusNotFound, "set contact for unknown user")
}
