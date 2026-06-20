package admin

import (
	"io"
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
	req, _ := http.NewRequest("PUT", ts.URL+path, strings.NewReader(form.Encode()))
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

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user detail status %d, want 200", resp.StatusCode)
	}
	if d.gotUser != "alice@hermex.test" {
		t.Errorf("GetUser called for %q, want alice@hermex.test", d.gotUser)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"alice@hermex.test", `name="status"`, `name="pop3_imap"`, "Delete user"} {
		if !strings.Contains(string(body), want) {
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user detail = %d, want 404", resp.StatusCode)
	}
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit status %d, want 200", resp.StatusCode)
	}
	if d.updatedUser != "alice@hermex.test" {
		t.Errorf("UpdateUser called for %q, want alice@hermex.test", d.updatedUser)
	}
	if d.updateUser.Status != 1 || d.updateUser.Lang != "de" || d.updateUser.DisplayType != 7 ||
		d.updateUser.Homeserver != 2 || !d.updateUser.POP3IMAP || d.updateUser.SMTP {
		t.Errorf("edit payload = %+v, want the submitted fields with smtp unchecked", d.updateUser)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d, want 200", resp.StatusCode)
	}
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

	req, _ := http.NewRequest("PUT", ts.URL+"/admin/ui/users/alice@hermex.test", strings.NewReader("status=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("edit without CSRF = %d, want 403", resp.StatusCode)
	}
	if d.updatedUser != "" {
		t.Errorf("a CSRF-less edit still updated %q", d.updatedUser)
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

	page := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(body), "ali") || !strings.Contains(string(body), "Alternative login names") {
		t.Errorf("detail page missing the altnames section/values:\n%s", body)
	}

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/altnames", session, csrf,
		url.Values{"altnames": {"newone\nnewtwo  three"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("altnames save status %d, want 200", resp.StatusCode)
	}
	if d.setAltnamesUser != "alice@hermex.test" || len(d.setAltnames) != 3 {
		t.Errorf("SetAltnames = (%q, %v), want alice@hermex.test split into 3 names", d.setAltnamesUser, d.setAltnames)
	}
	if rb, _ := io.ReadAll(resp.Body); !strings.Contains(string(rb), "Saved") {
		t.Errorf("altnames save response = %s, want a success acknowledgement", rb)
	}
}
