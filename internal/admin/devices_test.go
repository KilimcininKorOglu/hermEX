package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/activesync"
	"hermex/internal/directory"
)

// deviceUserDir is a system-admin directory whose one user has a known maildir,
// so the device handlers resolve the mailbox store path.
func deviceUserDir() *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"},
	}
}

// TestAdminGetUserDevices proves a system admin reads a user's ActiveSync devices,
// merged from the mailbox store and serialized as the canonical device info.
func TestAdminGetUserDevices(t *testing.T) {
	d := deviceUserDir()
	store := &fakeStore{devices: map[string][]activesync.DeviceInfo{
		"/mb/alice": {{DeviceID: "dev1", DeviceType: "iPhone", FoldersSynced: 3, WipeStatus: activesync.WipeStatusOK}},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/devices", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get devices status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"deviceId":"dev1"`) || !strings.Contains(string(body), `"foldersSynced":3`) {
		t.Errorf("devices body = %s, want dev1 listed", body)
	}
}

// TestAdminUserDeviceWipe proves a system admin queues a remote wipe through to
// the mailbox store at the user's maildir.
func TestAdminUserDeviceWipe(t *testing.T) {
	d := deviceUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/alice@hermex.test/devices/action", session, csrf,
		`{"deviceId":"dev1","action":"wipe"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("device wipe status %d, want 204", resp.StatusCode)
	}
	if store.deviceAction != "wipe" || store.deviceActionDir != "/mb/alice" || store.deviceActionID != "dev1" {
		t.Errorf("device action = %q dir=%q id=%q, want wipe /mb/alice dev1",
			store.deviceAction, store.deviceActionDir, store.deviceActionID)
	}
}

// TestAdminUserDevicesNotFound proves device read/action on an unknown user is a
// 404 and the store is never touched.
func TestAdminUserDevicesNotFound(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}, getUserMissing: true}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/ghost@hermex.test/devices", session)
	get.Body.Close()
	post := authedPOST(t, ts, "/admin/users/ghost@hermex.test/devices/action", session, csrf, `{"deviceId":"dev1","action":"resync"}`)
	post.Body.Close()
	if get.StatusCode != http.StatusNotFound || post.StatusCode != http.StatusNotFound {
		t.Errorf("unknown-user devices = GET %d / POST %d, want both 404", get.StatusCode, post.StatusCode)
	}
	if store.deviceAction != "" {
		t.Errorf("an unknown user still acted on the store: %q", store.deviceAction)
	}
}

// TestAdminUserDevicesRequiresSystem proves a domain admin cannot read or act on
// a user's devices through the system-scoped endpoints.
func TestAdminUserDevicesRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/devices", session)
	get.Body.Close()
	post := authedPOST(t, ts, "/admin/users/alice@hermex.test/devices/action", session, csrf, `{"deviceId":"dev1","action":"resync"}`)
	post.Body.Close()
	if get.StatusCode != http.StatusForbidden || post.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin devices = GET %d / POST %d, want both 403", get.StatusCode, post.StatusCode)
	}
}

// TestUIUserDetailShowsDevices proves the detail page renders the mobile-devices
// table populated from the store, with a pending-wipe device offering Cancel.
func TestUIUserDetailShowsDevices(t *testing.T) {
	d := deviceUserDir()
	store := &fakeStore{devices: map[string][]activesync.DeviceInfo{
		"/mb/alice": {{DeviceID: "phone-1", DeviceType: "iPhone", WipeStatus: activesync.WipeStatusPending}},
	}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Mobile devices", "phone-1", "Wipe pending", `value="cancel"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page devices section missing %q", want)
		}
	}
}

// TestUIUserDeviceAction proves a device action from the detail form goes through
// to the store and returns the refreshed device panel.
func TestUIUserDeviceAction(t *testing.T) {
	d := deviceUserDir()
	store := &fakeStore{devices: map[string][]activesync.DeviceInfo{
		"/mb/alice": {{DeviceID: "phone-1", DeviceType: "iPhone", WipeStatus: activesync.WipeStatusOK}},
	}}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/devices/action", session, csrf, url.Values{
		"deviceID": {"phone-1"},
		"action":   {"resync"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui device action status %d, want 200", resp.StatusCode)
	}
	if store.deviceAction != "resync" || store.deviceActionID != "phone-1" {
		t.Errorf("stored action = %q id=%q, want resync phone-1", store.deviceAction, store.deviceActionID)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "phone-1") {
		t.Errorf("device action did not return the refreshed panel:\n%s", body)
	}
}
