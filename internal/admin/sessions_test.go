package admin

import (
	"io"
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
	resp := authedGET(t, ts, "/admin/ui/mobile-devices", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mobile devices page status %d, want 200", resp.StatusCode)
	}
	s := string(body)
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
	resp := authedGET(t, ts, "/admin/ui/mobile-devices/panel", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panel status %d, want 200", resp.StatusCode)
	}
	s := string(body)
	if !strings.Contains(s, "alice@hermex.test") || !strings.Contains(s, `hx-trigger="every 2s"`) {
		t.Errorf("panel missing the session or the refresh trigger:\n%s", s)
	}
}

// TestUIMobileDevicesRequiresSystem proves an org admin cannot view the monitor.
func TestUIMobileDevicesRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/mobile-devices", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin monitor page = %d, want 403", resp.StatusCode)
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
	resp := authedGET(t, ts, "/admin/mobile-devices", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions JSON status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"username":"alice@hermex.test"`) || !strings.Contains(string(body), `"command":"Ping"`) {
		t.Errorf("sessions JSON = %s, want the live session", body)
	}
}
