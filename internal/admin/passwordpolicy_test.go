package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

func passwordPolicyDir(minLen int) *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "hermex.test"},
		minPwLen:     minLen,
	}
}

// TestSavePasswordPolicy proves the Limits form stores the minimum, the Settings
// page shows the stored value, and a value outside 1..MaxMinPasswordLength (or one
// that is not a number) is refused and leaves the stored one.
func TestSavePasswordPolicy(t *testing.T) {
	d := passwordPolicyDir(0)
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/limits", session), http.StatusOK, "limits page")
	wantContains(t, page, `name="min_password_length" value="8"`, "the default minimum before a save")

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/limits/passwordpolicy", session, csrf,
		url.Values{"min_password_length": {"12"}}), http.StatusOK, "save 12")
	wantContains(t, body, "Password length saved", "the save acknowledgement")
	if d.minPwLen != 12 {
		t.Fatalf("stored minimum = %d, want 12", d.minPwLen)
	}
	page = wantBody(t, authedGET(t, ts, "/admin/ui/settings", session), http.StatusOK, "settings page")
	wantContains(t, page, `name="min_password_length" value="12"`, "the stored minimum on the Settings page")

	for _, bad := range []string{"0", "-3", "129", "l2"} {
		body := wantBody(t, htmxPOST(t, ts, "/admin/ui/limits/passwordpolicy", session, csrf,
			url.Values{"min_password_length": {bad}}), http.StatusOK, bad)
		wantContains(t, body, "between 1 and 128", "the refusal of "+bad)
		if d.minPwLen != 12 {
			t.Errorf("%q changed the stored minimum to %d", bad, d.minPwLen)
		}
	}
}

// TestAPIPasswordRefusalsNameTheMinimum proves each JSON path that stores a
// password answers a too-short one with a 400 naming the minimum, not the 500 a
// server fault gets, and stores nothing.
func TestAPIPasswordRefusalsNameTheMinimum(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/admin/users", `{"email":"new@hermex.test","password":"elevenchars"}`},
		{"POST", "/admin/users/u@hermex.test/password", `{"password":"elevenchars"}`},
		{"PUT", "/admin/passwd", `{"old":"pw","new":"elevenchars"}`},
	} {
		d := passwordPolicyDir(12)
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)
		body := wantBody(t, authedReq(t, ts, tc.method, tc.path, session, csrf, tc.body), http.StatusBadRequest, tc.path)
		wantContains(t, body, "at least 12 characters", tc.path+": the minimum in the refusal")
		if d.createdUser != "" || d.setPwValue != "" {
			t.Errorf("%s stored a refused password (user %q, password %q)", tc.path, d.createdUser, d.setPwValue)
		}
	}
}

// TestPanelPasswordRefusalsNameTheMinimum proves the panel's create form and its
// own-password form show the minimum when the password is too short, rather than
// the generic failure that gives the operator nothing to correct.
func TestPanelPasswordRefusalsNameTheMinimum(t *testing.T) {
	d := passwordPolicyDir(12)
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	body := wantBody(t, htmxPOST(t, ts, "/admin/ui/users", session, csrf,
		url.Values{"local": {"new"}, "domain": {"1"}, "password": {"elevenchars"}}), http.StatusOK, "create")
	wantContains(t, body, "at least 12 characters", "the create form's refusal")
	if d.createdUser != "" {
		t.Errorf("a too-short password created %q", d.createdUser)
	}

	body = wantBody(t, htmxPUT(t, ts, "/admin/ui/change-password", session, csrf,
		url.Values{"old": {"pw"}, "new": {"elevenchars"}}), http.StatusOK, "own change")
	wantContains(t, body, "at least 12 characters", "the own-password form's refusal")
	if d.setPwValue != "" {
		t.Errorf("a too-short own password was stored: %q", d.setPwValue)
	}
}
