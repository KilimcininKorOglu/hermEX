package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
)

// TestAdminGetUserSyncPolicy proves a system admin reads a user's device-policy
// override as the partial field map.
func TestAdminGetUserSyncPolicy(t *testing.T) {
	store := &fakeStore{syncPolicy: map[string]easpolicy.Policy{
		"/mb/alice": {"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8},
	}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/syncpolicy", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get sync policy status %d, want 200", resp.StatusCode)
	}
	var got easpolicy.Policy
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["DevicePasswordEnabled"] != 1 || got["MinDevicePasswordLength"] != 8 {
		t.Errorf("sync policy = %v, want the override", got)
	}
}

// TestAdminSetUserSyncPolicy proves a system admin writes the override and that an
// unknown field is refused rather than stored.
func TestAdminSetUserSyncPolicy(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/syncpolicy", session, csrf,
		`{"DevicePasswordEnabled":1,"MaxInactivityTimeDeviceLock":900}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set sync policy status %d, want 204", resp.StatusCode)
	}
	if store.setSyncDir != "/mb/alice" || store.setSyncPolicy["DevicePasswordEnabled"] != 1 || store.setSyncPolicy["MaxInactivityTimeDeviceLock"] != 900 {
		t.Errorf("stored = %q/%v, want the policy at /mb/alice", store.setSyncDir, store.setSyncPolicy)
	}

	bad := authedPUT(t, ts, "/admin/users/alice@hermex.test/syncpolicy", session, csrf, `{"NotAField":1}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown-field status %d, want 400", bad.StatusCode)
	}
}

// TestAdminUserSyncPolicyRequiresSystem proves a domain admin cannot read or write it.
func TestAdminUserSyncPolicyRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/syncpolicy", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/syncpolicy", session, csrf, `{"DevicePasswordEnabled":1}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin sync policy = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserSyncPolicy proves the detail-form save reads the tri-state controls: a set
// field is stored, a blank field is omitted (inherits), and success is reported.
func TestUIUserSyncPolicy(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/syncpolicy", session, csrf, url.Values{
		"DevicePasswordEnabled":   {"1"},
		"MinDevicePasswordLength": {"6"},
		"AllowCamera":             {""}, // inherit
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui sync policy save status %d, want 200", resp.StatusCode)
	}
	got := store.setSyncPolicy
	if got["DevicePasswordEnabled"] != 1 || got["MinDevicePasswordLength"] != 6 {
		t.Errorf("stored = %v, want the set fields", got)
	}
	if _, set := got["AllowCamera"]; set {
		t.Errorf("a blank field was stored: %v (should inherit)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui save did not report success:\n%s", body)
	}
}

// TestAdminGetDefaultSyncPolicy proves a system admin reads the server-wide default
// device policy.
func TestAdminGetDefaultSyncPolicy(t *testing.T) {
	d := folderUserDir()
	d.defaultSyncPolicy = easpolicy.Policy{"DevicePasswordEnabled": 1, "MaxInactivityTimeDeviceLock": 600}
	ts := adminServerStore(t, d, &fakeStore{})
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/syncpolicy", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get default policy status %d, want 200", resp.StatusCode)
	}
	var got easpolicy.Policy
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["DevicePasswordEnabled"] != 1 || got["MaxInactivityTimeDeviceLock"] != 600 {
		t.Errorf("default policy = %v, want the configured baseline", got)
	}
}

// TestAdminSetDefaultSyncPolicy proves a system admin writes the default and an unknown
// field is refused.
func TestAdminSetDefaultSyncPolicy(t *testing.T) {
	d := folderUserDir()
	ts := adminServerStore(t, d, &fakeStore{})
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/syncpolicy", session, csrf, `{"DevicePasswordEnabled":1,"MinDevicePasswordLength":6}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set default policy status %d, want 204", resp.StatusCode)
	}
	if d.defaultSyncPolicy["DevicePasswordEnabled"] != 1 || d.defaultSyncPolicy["MinDevicePasswordLength"] != 6 {
		t.Errorf("stored default = %v, want the policy", d.defaultSyncPolicy)
	}

	bad := authedPUT(t, ts, "/admin/syncpolicy", session, csrf, `{"Nope":1}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown-field status %d, want 400", bad.StatusCode)
	}
}

// TestAdminDefaultSyncPolicyRequiresSystem proves a domain admin cannot read or write
// the server-wide default.
func TestAdminDefaultSyncPolicyRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/syncpolicy", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/syncpolicy", session, csrf, `{"DevicePasswordEnabled":1}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin default policy = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUISyncPolicyPage proves the server-default editor page renders the fields with the
// configured baseline pre-filled.
func TestUISyncPolicyPage(t *testing.T) {
	d := folderUserDir()
	d.defaultSyncPolicy = easpolicy.Policy{"MinDevicePasswordLength": 6}
	ts := adminServerStore(t, d, &fakeStore{})
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/syncpolicy", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"device policy (server default)", `name="MinDevicePasswordLength"`, `value="6"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("server-default page missing %q", want)
		}
	}
}

// TestUISaveSyncPolicy proves the server-default editor save writes the policy and
// reports success.
func TestUISaveSyncPolicy(t *testing.T) {
	d := folderUserDir()
	ts := adminServerStore(t, d, &fakeStore{})
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/syncpolicy", session, csrf, url.Values{
		"DevicePasswordEnabled":       {"1"},
		"MaxInactivityTimeDeviceLock": {"900"},
		"AllowBluetooth":              {""}, // unset
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save default policy status %d, want 200", resp.StatusCode)
	}
	if d.defaultSyncPolicy["DevicePasswordEnabled"] != 1 || d.defaultSyncPolicy["MaxInactivityTimeDeviceLock"] != 900 {
		t.Errorf("stored default = %v, want the set fields", d.defaultSyncPolicy)
	}
	if _, set := d.defaultSyncPolicy["AllowBluetooth"]; set {
		t.Errorf("an unset field was stored: %v", d.defaultSyncPolicy)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("save did not report success:\n%s", body)
	}
}

// TestUIUserDetailShowsSyncPolicy proves the detail page renders the policy editor with
// the stored override pre-filled.
func TestUIUserDetailShowsSyncPolicy(t *testing.T) {
	store := &fakeStore{syncPolicy: map[string]easpolicy.Policy{
		"/mb/alice": {"MinDevicePasswordLength": 8},
	}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"ActiveSync device policy", `name="DevicePasswordEnabled"`, `name="MinDevicePasswordLength"`, `value="8"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page sync-policy editor missing %q", want)
		}
	}
}
