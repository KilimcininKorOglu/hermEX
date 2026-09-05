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

// saveAutoReplyPrefix posts the prefix form the way the panel does, with the
// CSRF token in both the cookie and the header.
func saveAutoReplyPrefix(t *testing.T, ts *httptest.Server, session, csrf, prefix string) int {
	t.Helper()
	form := url.Values{"subject_prefix": {prefix}}.Encode()
	req, _ := http.NewRequest("POST", ts.URL+"/admin/ui/antispam/autoreply", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	req.Header.Set(csrfHeader, csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestTheSettingsPageOffersTheAutoReplyPrefix pairs the route with the control
// that posts to it: a panel route nothing posts to is unreachable, and a form
// that posts to nothing saves nothing.
func TestTheSettingsPageOffersTheAutoReplyPrefix(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/settings", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)
	if !strings.Contains(page, "/admin/ui/antispam/autoreply") {
		t.Fatal("the settings page does not reference the auto-reply route")
	}
	if !strings.Contains(page, directory.DefaultAutoReplySubjectPrefix) {
		t.Error("the form does not show the built-in default prefix")
	}

	if code := saveAutoReplyPrefix(t, ts, session, csrf, "Otomatik yanıt"); code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}
	if !d.autoReplyFound || d.autoReply.SubjectPrefix != "Otomatik yanıt" {
		t.Errorf("stored settings = %+v found %v", d.autoReply, d.autoReplyFound)
	}
}

// TestAnEmptyPrefixRestoresTheDefault keeps an operator who clears the field
// from sending every reply with no subject at all.
func TestAnEmptyPrefixRestoresTheDefault(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	if code := saveAutoReplyPrefix(t, ts, session, csrf, "   "); code != http.StatusOK {
		t.Fatalf("save = %d", code)
	}
	if d.autoReply.SubjectPrefix != directory.DefaultAutoReplySubjectPrefix {
		t.Errorf("an empty prefix stored %q", d.autoReply.SubjectPrefix)
	}
}
