package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// htmxPUT issues an htmx-style PUT with the session and CSRF cookies plus the
// X-CSRF-Token header (the double-submit htmx sends on hx-put).
func htmxPUT(t *testing.T, ts *httptest.Server, path, session, csrf string, form url.Values) *http.Response {
	t.Helper()
	req := newRequest(t, "PUT", ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(csrfHeader, csrf)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestUIUserDetail proves the detail page renders one user's account fields in an
// editable form, with the current status preselected and the delete control
// present, for a system admin.
func TestUIUserDetail(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Status: 1, Lang: "de", DisplayType: 7, POP3IMAP: true},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	unauthReq := newRequest(t, "GET", ts.URL+"/admin/ui/users/alice@hermex.test", nil)
	unauthResp, err := noRedirectClient().Do(unauthReq)
	mustNoErr(t, err, "unauth user detail")
	closeBody(t, unauthResp.Body)
	if unauthResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauth user detail = %d, want 303", unauthResp.StatusCode)
	}

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "user detail")
	if d.gotUser != "alice@hermex.test" {
		t.Errorf("GetUser called for %q, want alice@hermex.test", d.gotUser)
	}
	for _, want := range []string{"alice@hermex.test", `name="status"`, `name="pop3_imap"`, "Delete user"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q:\n%s", want, body)
		}
	}
}

// TestUIUserDetailNotFound proves an unknown user's detail page is a 404.
func TestUIUserDetailNotFound(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		getUserMissing: true,
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/ghost@hermex.test", session)
	wantStatus(t, resp, http.StatusNotFound, "unknown user detail")
}

// TestUIUserEdit proves the edit form saves the whole account subset through the
// directory and reports success; an unchecked checkbox clears its flag.
func TestUIUserEdit(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test", session, csrf, url.Values{
		"status":      {"1"},
		"lang":        {"de"},
		"timezone":    {"Europe/Berlin"},
		"displayType": {"7"},
		"homeserver":  {"2"},
		"pop3_imap":   {"on"},
	})
	body := wantBody(t, resp, http.StatusOK, "edit user")
	if d.updatedUser != "alice@hermex.test" {
		t.Errorf("UpdateUser called for %q, want alice@hermex.test", d.updatedUser)
	}
	if d.updateUser.Status != 1 || d.updateUser.Lang != "de" || d.updateUser.DisplayType != 7 ||
		d.updateUser.Homeserver != 2 || !d.updateUser.POP3IMAP || d.updateUser.SMTP {
		t.Errorf("edit payload = %+v, want the submitted fields with smtp unchecked", d.updateUser)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("edit response = %s, want a success acknowledgement", body)
	}
}

// TestUIUserDelete proves the delete control removes the user, carries the
// deleteFiles intent, and redirects htmx back to the user list.
func TestUIUserDelete(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/delete", session, csrf,
		url.Values{"deleteFiles": {"on"}})
	wantStatus(t, resp, http.StatusOK, "delete user")
	if d.deletedUser != "alice@hermex.test" || !d.deleteFiles {
		t.Errorf("DeleteUser = (%q, files=%v), want (alice@hermex.test, true)", d.deletedUser, d.deleteFiles)
	}
	if loc := resp.Header.Get("HX-Redirect"); loc != "/admin/ui/users" {
		t.Errorf("HX-Redirect = %q, want /admin/ui/users", loc)
	}
}

// TestUIUserEditRequiresCSRF proves the edit endpoint refuses a request that
// carries the session but no double-submit CSRF token.
func TestUIUserEditRequiresCSRF(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	req := newRequest(t, "PUT", ts.URL+"/admin/ui/users/alice@hermex.test", strings.NewReader("status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	mustNoErr(t, err, "CSRF-less edit")
	wantStatus(t, resp, http.StatusForbidden, "edit without CSRF")
	if d.updatedUser != "" {
		t.Errorf("a CSRF-less edit still updated %q", d.updatedUser)
	}
}

// TestUIUserDetailFormsNeedASystemAdmin proves every user-detail form is refused to
// a domain admin and none reaches the directory. A role grant matters most: a domain
// admin able to grant itself the system role would hold the whole server.
func TestUIUserDetailFormsNeedASystemAdmin(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		userDetail: directory.UserDetail{ID: 7, Username: "admin@hermex.test", DomainID: 1}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	const user = "/admin/ui/users/admin@hermex.test"
	wantStatus(t, authedGET(t, ts, user, session), http.StatusForbidden, "detail page")
	for _, c := range []struct {
		method, path string
		form         url.Values
	}{
		{"POST", user + "/roles/grant", url.Values{"role": {"system"}}},
		{"POST", user + "/roles/revoke", url.Values{"role": {"domain"}, "scopeID": {"1"}}},
		{"PUT", user, url.Values{"status": {"0"}}},
		{"POST", user + "/delete", url.Values{"deleteFiles": {"on"}}},
		{"PUT", user + "/altnames", url.Values{"altnames": {"ceo"}}},
		{"PUT", user + "/aliases", url.Values{"aliases": {"ceo@other.test"}}},
		{"PUT", user + "/contact", url.Values{"displayname": {"CEO"}}},
	} {
		var resp *http.Response
		if c.method == "PUT" {
			resp = htmxPUT(t, ts, c.path, session, csrf, c.form)
		} else {
			resp = htmxPOST(t, ts, c.path, session, csrf, c.form)
		}
		wantStatus(t, resp, http.StatusForbidden, c.method+" "+c.path)
	}
	if d.grantedRole != "" || d.revokedRole != "" || d.updatedUser != "" || d.deletedUser != "" ||
		d.setAltnamesUser != "" || d.setAliasesUser != "" || d.setPropsUser != "" {
		t.Errorf("a refused form reached the directory: grant %q revoke %q update %q delete %q altnames %q aliases %q props %q",
			d.grantedRole, d.revokedRole, d.updatedUser, d.deletedUser, d.setAltnamesUser, d.setAliasesUser, d.setPropsUser)
	}
}

// TestUIUserAltnames proves the detail page seeds the alternative-names textarea
// with the current set and the save splits the textarea on whitespace before
// replacing the set through the directory.
func TestUIUserAltnames(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test"},
		altnames:   []string{"ali", "alice2"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "altnames page")
	if !strings.Contains(body, "ali") || !strings.Contains(body, "Alternative login names") {
		t.Errorf("detail page missing the altnames section/values:\n%s", body)
	}

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/altnames", session, csrf,
		url.Values{"altnames": {"newone\nnewtwo  three"}})
	saveBody := wantBody(t, resp, http.StatusOK, "altnames save")
	if d.setAltnamesUser != "alice@hermex.test" || len(d.setAltnames) != 3 {
		t.Errorf("SetAltnames = (%q, %v), want alice@hermex.test split into 3 names", d.setAltnamesUser, d.setAltnames)
	}
	if !strings.Contains(saveBody, "Saved") {
		t.Errorf("altnames save response = %s, want a success acknowledgement", saveBody)
	}
}

// TestUIUserAliases proves the detail page seeds the aliases textarea and the
// save splits on whitespace before replacing the set through the directory.
func TestUIUserAliases(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail:  directory.UserDetail{Username: "alice@hermex.test"},
		userAliases: []string{"info@hermex.test", "sales@hermex.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "aliases page")
	if !strings.Contains(body, "sales@hermex.test") || !strings.Contains(body, "E-mail aliases") {
		t.Errorf("detail page missing the aliases section/values:\n%s", body)
	}

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/aliases", session, csrf,
		url.Values{"aliases": {"a@hermex.test\nb@hermex.test"}})
	if d.setAliasesUser != "alice@hermex.test" || len(d.setAliases) != 2 {
		t.Errorf("SetAliasesFor = (%q, %v), want alice@hermex.test with 2 aliases", d.setAliasesUser, d.setAliases)
	}
	wantStatus(t, resp, http.StatusOK, "aliases save")
}

// TestUIUserRoles proves the detail page shows the admin-roles panel and that the
// grant and revoke controls carry the role and its scope to the directory.
func TestUIUserRoles(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{ID: 5, Username: "alice@hermex.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "roles page")
	if !strings.Contains(body, "Grant role") || !strings.Contains(body, "Admin roles") {
		t.Errorf("detail page missing the roles panel:\n%s", body)
	}

	grant := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/roles/grant", session, csrf,
		url.Values{"role": {"domain"}, "scopeID": {"3"}})
	wantStatus(t, grant, http.StatusOK, "grant role")
	if d.grantedRole != "domain" || d.grantedScope != 3 {
		t.Errorf("granted (%q, %d), want (domain, 3)", d.grantedRole, d.grantedScope)
	}

	revoke := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/roles/revoke", session, csrf,
		url.Values{"role": {"domain"}, "scopeID": {"3"}})
	wantStatus(t, revoke, http.StatusOK, "revoke role")
	if d.revokedRole != "domain" || d.revokedScope != 3 {
		t.Errorf("revoked (%q, %d), want (domain, 3)", d.revokedRole, d.revokedScope)
	}

	// A scope that is not a number must reach the directory as the refused -1, not as
	// 0, which stored a domain role that grants nothing yet showed as granted.
	badScope := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/roles/grant", session, csrf,
		url.Values{"role": {"domain"}, "scopeID": {"acme"}})
	wantStatus(t, badScope, http.StatusOK, "bad scope")
	if d.grantedScope != -1 {
		t.Errorf("a forged scope reached the directory as %d, want -1", d.grantedScope)
	}
}

// TestUIUserContact proves the detail page seeds the contact form with stored
// values and the save maps the named form fields to their proptags in the
// directory.
func TestUIUserContact(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test"},
		userProps:  map[uint32]string{0x3001001F: "Alice Liddell"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "contact page")
	if !strings.Contains(body, "Contact details") || !strings.Contains(body, "Alice Liddell") {
		t.Errorf("detail page missing the contact section/values:\n%s", body)
	}

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/contact", session, csrf, url.Values{
		"displayname": {"Alice L."},
		"title":       {"Curiouser"},
	})
	wantStatus(t, resp, http.StatusOK, "contact save")
	if d.setPropsUser != "alice@hermex.test" || d.setProps[0x3001001F] != "Alice L." || d.setProps[0x3A17001F] != "Curiouser" {
		t.Errorf("SetUserProperties = (%q, %v), want the form fields mapped to proptags", d.setPropsUser, d.setProps)
	}
}
