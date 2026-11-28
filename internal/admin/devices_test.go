package admin

import (
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

	unauth := authedGET(t, ts, "/admin/users/alice@hermex.test/devices", "invalid-session")
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated devices GET")

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/devices", session)
	body := wantBody(t, resp, http.StatusOK, "get devices")
	if !strings.Contains(body, `"deviceId":"dev1"`) || !strings.Contains(body, `"foldersSynced":3`) {
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
	wantStatus(t, resp, http.StatusNoContent, "device wipe")
	if store.deviceAction != "wipe" || store.deviceActionDir != "/mb/alice" || store.deviceActionID != "dev1" {
		t.Errorf("device action = %q dir=%q id=%q, want wipe /mb/alice dev1",
			store.deviceAction, store.deviceActionDir, store.deviceActionID)
	}
}

// TestAdminUserDeviceUnknownAction sends an action outside the documented set
// (resync, delete, wipe, wipe-account, cancel): it is a 400 and the store is never
// asked to act on the device.
func TestAdminUserDeviceUnknownAction(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, deviceUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/alice@hermex.test/devices/action", session, csrf,
		`{"deviceId":"dev1","action":"format-disk"}`)
	wantStatus(t, resp, http.StatusBadRequest, "unknown action")
	if store.deviceAction != "" {
		t.Errorf("the store was asked to %q the device", store.deviceAction)
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
	wantStatus(t, get, http.StatusNotFound, "unknown user get")
	post := authedPOST(t, ts, "/admin/users/ghost@hermex.test/devices/action", session, csrf, `{"deviceId":"dev1","action":"resync"}`)
	wantStatus(t, post, http.StatusNotFound, "unknown user post")
	if store.deviceAction != "" {
		t.Errorf("an unknown user still acted on the store: %q", store.deviceAction)
	}
}

// TestAdminUserDevicesRequiresSystem proves a domain admin cannot read or act on the
// devices of a user outside its scope through the user-scoped API, and cannot use
// the panel form, which is system-only.
func TestAdminUserDevicesRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/devices", session)
	wantStatus(t, get, http.StatusForbidden, "domain admin get")
	post := authedPOST(t, ts, "/admin/users/alice@hermex.test/devices/action", session, csrf, `{"deviceId":"dev1","action":"wipe"}`)
	wantStatus(t, post, http.StatusForbidden, "domain admin post")
	ui := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/devices/action", session, csrf,
		url.Values{"deviceID": {"dev1"}, "action": {"wipe"}})
	wantStatus(t, ui, http.StatusForbidden, "domain-admin UI device action")
	if store.deviceAction != "" {
		t.Errorf("a domain admin had the store %q a device", store.deviceAction)
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
	body := wantBody(t, resp, http.StatusOK, "detail devices")
	for _, want := range []string{"Mobile devices", "phone-1", "Wipe pending", `value="cancel"`} {
		if !strings.Contains(body, want) {
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
	body := wantBody(t, resp, http.StatusOK, "ui device action")
	if store.deviceAction != "resync" || store.deviceActionID != "phone-1" {
		t.Errorf("stored action = %q id=%q, want resync phone-1", store.deviceAction, store.deviceActionID)
	}
	if !strings.Contains(body, "phone-1") {
		t.Errorf("device action did not return the refreshed panel:\n%s", body)
	}
}
