package admin

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

func sampleSessions() []directory.SessionRecord {
	return []directory.SessionRecord{
		{ID: "s1", Username: "alice@hermex.test", IP: "10.0.0.5", DeviceType: "iPhone",
			DeviceID: "dev1", Command: "Ping", ASVersion: "14.1", Push: true, LastUpdate: 1},
	}
}

// TestUIMobileDevicesPage proves the monitor page lists a live session and wires
// the auto-refresh poll.
func TestUIMobileDevicesPage(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		activeSessions: sampleSessions(),
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	s := wantBody(t, authedGET(t, ts, "/admin/ui/mobile-devices", session), http.StatusOK, "mobile devices page")
	for _, want := range []string{"alice@hermex.test", "iPhone", "Ping", "badge", `hx-trigger="every 2s"`} {
		if !strings.Contains(s, want) {
			t.Errorf("mobile devices page missing %q:\n%s", want, s)
		}
	}
}

// TestUIMobileDevicesPanel proves the poll endpoint returns the session table and
// keeps the refresh trigger so it polls again.
func TestUIMobileDevicesPanel(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		activeSessions: sampleSessions(),
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	s := wantBody(t, authedGET(t, ts, "/admin/ui/mobile-devices/panel", session), http.StatusOK, "mobile devices panel")
	if !strings.Contains(s, "alice@hermex.test") || !strings.Contains(s, `hx-trigger="every 2s"`) {
		t.Errorf("panel missing the session or the refresh trigger:\n%s", s)
	}
}

// TestUIMobileDevicesReportsAReadFailure proves a failed session read says so on
// the page and on the poll. It used to render as "No active sessions", which reads
// as no device being connected.
func TestUIMobileDevicesReportsAReadFailure(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		activeSessionsErr: errors.New("directory unreachable"),
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	for _, path := range []string{"/admin/ui/mobile-devices", "/admin/ui/mobile-devices/panel"} {
		page := wantBody(t, authedGET(t, ts, path, session), http.StatusOK, path)
		wantContains(t, page, "Could not read the sessions", path+" reports the failed read")
		if strings.Contains(page, "No active sessions") {
			t.Errorf("%s: a failed read rendered as no active sessions:\n%s", path, page)
		}
	}
}

// TestUIMobileDevicesRequiresSystem proves an org admin cannot view the monitor.
func TestUIMobileDevicesRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}},
		activeSessions: sampleSessions()}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	// Every surface carrying the session table is refused, since each one lists
	// other domains' users and their client addresses.
	for _, path := range []string{"/admin/ui/mobile-devices", "/admin/ui/mobile-devices/panel", "/admin/mobile-devices"} {
		body := wantBody(t, authedGET(t, ts, path, session), http.StatusForbidden, path)
		if strings.Contains(body, "10.0.0.5") {
			t.Errorf("%s: the refusal carries a session's address", path)
		}
	}
}

// TestAdminMobileDevicesJSON proves the JSON endpoint returns the live sessions.
func TestAdminMobileDevicesJSON(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		activeSessions: sampleSessions(),
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	body := wantBody(t, authedGET(t, ts, "/admin/mobile-devices", session), http.StatusOK, "sessions JSON")
	if !strings.Contains(body, `"username":"alice@hermex.test"`) || !strings.Contains(body, `"command":"Ping"`) {
		t.Errorf("sessions JSON = %s, want the live session", body)
	}

	unauthResp, err := http.Get(ts.URL + "/admin/mobile-devices")
	mustNoErr(t, err, "unauthenticated mobile-devices request")
	wantStatus(t, unauthResp, http.StatusUnauthorized, "unauthenticated mobile-devices")
}
