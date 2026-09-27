package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAccountZoneSaveAndShow proves an operator's time zone is stored in the
// users record, cached in the zone cookie, and shown on the account page.
func TestAccountZoneSaveAndShow(t *testing.T) {
	d := cpFakeDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/account/timezone", session, csrf, url.Values{"timezone": {"Europe/Istanbul"}})
	cookie := resp.Header.Get("Set-Cookie")
	body := wantBody(t, resp, http.StatusOK, "save the zone")
	wantContains(t, body, "Times are now shown in Europe/Istanbul.", "the acknowledgement")
	wantEq(t, d.uiPrefs["admin@hermex.test"].Timezone, "Europe/Istanbul", "the stored zone")
	wantContains(t, cookie, zoneCookie+"=Europe/Istanbul", "the zone cookie")

	page := wantBody(t, authedGET(t, ts, "/admin/ui/change-password", session), http.StatusOK, "account page")
	wantContains(t, page, `value="Europe/Istanbul"`, "the page shows the stored zone")
}

// TestAccountZoneRefusesAnUnknownName proves a name the zone database does not
// know is refused and the stored zone is kept.
func TestAccountZoneRefusesAnUnknownName(t *testing.T) {
	d := cpFakeDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	htmxPUT(t, ts, "/admin/ui/account/timezone", session, csrf, url.Values{"timezone": {"Europe/Istanbul"}}).Body.Close()
	for _, name := range []string{"Mars/Olympus", "Local"} {
		body := wantBody(t, htmxPUT(t, ts, "/admin/ui/account/timezone", session, csrf, url.Values{"timezone": {name}}),
			http.StatusOK, "save "+name)
		wantContains(t, body, "not a known time zone name", "the refusal of "+name)
	}
	wantEq(t, d.uiPrefs["admin@hermex.test"].Timezone, "Europe/Istanbul", "the stored zone is kept")
}

// TestAccountZoneReadFailureHidesTheForm proves an unreadable zone hides the
// form, because saving the empty field would clear the stored zone.
func TestAccountZoneReadFailureHidesTheForm(t *testing.T) {
	d := cpFakeDir()
	d.readErrs = map[string]error{"GetUserPrefs": errors.New("db down")}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	page := wantBody(t, authedGET(t, ts, "/admin/ui/change-password", session), http.StatusOK, "account page")
	wantContains(t, page, "Could not read your time zone.", "the read failure")
	if strings.Contains(page, `name="timezone"`) {
		t.Errorf("the zone form is shown after a failed read")
	}
}

// TestRequestZone proves the render zone comes from the stored choice first,
// then the cookie, and falls back to UTC for an empty or unknown name.
func TestRequestZone(t *testing.T) {
	ist, err := time.LoadLocation("Europe/Istanbul")
	mustNoErr(t, err, "load a zone")
	withCookie := func(v string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin/ui/", nil)
		r.AddCookie(&http.Cookie{Name: zoneCookie, Value: v})
		return r
	}
	wantEq(t, requestZone(withCookie("Europe/Istanbul")).String(), ist.String(), "the cookie zone")
	wantEq(t, requestZone(withCookie("Mars/Olympus")), time.UTC, "an unknown cookie zone")
	wantEq(t, requestZone(httptest.NewRequest(http.MethodGet, "/", nil)), time.UTC, "no zone")
	wantEq(t, requestZone(withZone(withCookie("Europe/Istanbul"), "")), time.UTC, "a stored empty zone wins over the cookie")
}
