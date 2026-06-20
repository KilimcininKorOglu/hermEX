package activesync

import (
	"testing"

	"hermex/internal/objectstore"
)

// deviceTestStore opens a fresh per-test mailbox store.
func deviceTestStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestRecordDeviceContact proves the first contact stamps firstSync and an OK
// wipe status, and a later contact refreshes lastSync and the live attributes
// without resetting firstSync or clearing an outstanding wipe status — otherwise
// every subsequent sync would silently cancel an administrator's pending wipe.
func TestRecordDeviceContact(t *testing.T) {
	st := deviceTestStore(t)

	if _, err := recordDeviceContact(st, "dev1", "alice@hermex.test", "iPhone", "Apple-iPhone/1", "14.1", 1000); err != nil {
		t.Fatalf("first record: %v", err)
	}
	m, err := loadDevices(st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	d := m.Devices["dev1"]
	if d == nil || d.FirstSync != 1000 || d.LastSync != 1000 || d.WipeStatus != WipeStatusOK ||
		d.DeviceType != "iPhone" || d.UserAgent != "Apple-iPhone/1" || d.ASVersion != "14.1" || d.DeviceUser != "alice@hermex.test" {
		t.Fatalf("first record meta = %+v, want all stamped fields", d)
	}

	// Simulate an administrator-requested wipe, then a later contact.
	d.WipeStatus = WipeStatusPending
	if err := saveDevices(st, m); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := recordDeviceContact(st, "dev1", "alice@hermex.test", "iPhone", "Apple-iPhone/2", "14.1", 2000); err != nil {
		t.Fatalf("second record: %v", err)
	}
	m, _ = loadDevices(st)
	d = m.Devices["dev1"]
	if d.FirstSync != 1000 || d.LastSync != 2000 || d.WipeStatus != WipeStatusPending || d.UserAgent != "Apple-iPhone/2" {
		t.Fatalf("second record meta = %+v, want firstSync/wipe preserved and lastSync/agent refreshed", d)
	}
}

// setDeviceWipe forces a device's stored remote-wipe status.
func setDeviceWipe(t *testing.T, st *objectstore.Store, id string, status int) {
	t.Helper()
	m, err := loadDevices(st)
	if err != nil {
		t.Fatalf("load devices: %v", err)
	}
	m.device(id).WipeStatus = status
	if err := saveDevices(st, m); err != nil {
		t.Fatalf("save devices: %v", err)
	}
}

// deviceWipe reads a device's stored remote-wipe status.
func deviceWipe(t *testing.T, st *objectstore.Store, id string) int {
	t.Helper()
	m, err := loadDevices(st)
	if err != nil {
		t.Fatalf("load devices: %v", err)
	}
	if d := m.Devices[id]; d != nil {
		return d.WipeStatus
	}
	return WipeStatusUnknown
}

// TestAdvanceProvisionWipe proves the remote-wipe lifecycle across Provision
// exchanges: a device with no outstanding wipe emits nothing (so a normal
// Provision never carries a spurious directive), a pending wipe emits the wipe
// element and moves to requested, an acknowledgement moves it to wiped, and an
// account-only wipe takes the account path.
func TestAdvanceProvisionWipe(t *testing.T) {
	st := deviceTestStore(t)

	if _, err := recordDeviceContact(st, "dev1", "alice@hermex.test", "iPhone", "ua", "14.1", 1000); err != nil {
		t.Fatalf("seed dev1: %v", err)
	}
	if emit, err := advanceProvisionWipe(st, "dev1", false); err != nil || emit != wipeEmitNone {
		t.Fatalf("no-wipe device = (%d,%v), want wipeEmitNone", emit, err)
	}

	setDeviceWipe(t, st, "dev1", WipeStatusPending)
	if emit, err := advanceProvisionWipe(st, "dev1", false); err != nil || emit != wipeEmitFull {
		t.Fatalf("pending wipe = (%d,%v), want wipeEmitFull", emit, err)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusRequested {
		t.Errorf("after delivery status = %d, want requested(%d)", got, WipeStatusRequested)
	}
	if emit, _ := advanceProvisionWipe(st, "dev1", true); emit != wipeEmitFull {
		t.Errorf("acked wipe emit = %d, want wipeEmitFull", emit)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusWiped {
		t.Errorf("after ack status = %d, want wiped(%d)", got, WipeStatusWiped)
	}

	// Wiped is terminal: a further exchange emits nothing and stays wiped, so a
	// device that reconnects is not wiped again in a loop.
	if emit, err := advanceProvisionWipe(st, "dev1", false); err != nil || emit != wipeEmitNone {
		t.Errorf("post-wiped advance = (%d,%v), want wipeEmitNone", emit, err)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusWiped {
		t.Errorf("post-wiped status = %d, want it to stay wiped(%d)", got, WipeStatusWiped)
	}

	setDeviceWipe(t, st, "dev1", WipeStatusAccountPending)
	if emit, _ := advanceProvisionWipe(st, "dev1", false); emit != wipeEmitAccount {
		t.Errorf("account-only pending emit = %d, want wipeEmitAccount", emit)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusAccountRequested {
		t.Errorf("after account delivery status = %d, want account-requested(%d)", got, WipeStatusAccountRequested)
	}
}

// TestDeviceMutations proves the management actions on device state: resync
// clears a device's sync state but keeps it listed, a queued wipe survives a
// later contact (so it actually reaches the device) and can be cancelled, an
// account-only wipe takes the account-pending status, and delete removes the
// device entirely.
func TestDeviceMutations(t *testing.T) {
	st := deviceTestStore(t)

	if _, err := recordDeviceContact(st, "dev1", "alice@hermex.test", "iPhone", "ua", "14.1", 1000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	state, _ := loadState(st)
	state.device("dev1").collection("1").SyncKey = "1"
	state.device("dev1").collection("2").SyncKey = "1"
	if err := saveState(st, state); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := ResyncDevice(st, "dev1"); err != nil {
		t.Fatalf("resync: %v", err)
	}
	devs, _ := Devices(st)
	if len(devs) != 1 || devs[0].FoldersSynced != 0 {
		t.Fatalf("after resync devices = %+v, want dev1 listed with 0 folders", devs)
	}

	if err := RequestWipe(st, "dev1", false); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if _, err := recordDeviceContact(st, "dev1", "alice@hermex.test", "iPhone", "ua", "14.1", 2000); err != nil {
		t.Fatalf("contact after wipe: %v", err)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusPending {
		t.Errorf("queued wipe after contact = %d, want pending(%d) — a contact must not cancel a wipe", got, WipeStatusPending)
	}
	if err := CancelWipe(st, "dev1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusOK {
		t.Errorf("after cancel = %d, want OK(%d)", got, WipeStatusOK)
	}

	if err := RequestWipe(st, "dev1", true); err != nil {
		t.Fatalf("account wipe: %v", err)
	}
	if got := deviceWipe(t, st, "dev1"); got != WipeStatusAccountPending {
		t.Errorf("account wipe = %d, want account-pending(%d)", got, WipeStatusAccountPending)
	}

	if err := DeleteDevice(st, "dev1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	devs, _ = Devices(st)
	if len(devs) != 0 {
		t.Errorf("after delete devices = %+v, want empty", devs)
	}
}

// TestRecordDeviceContactBlank proves a request with no device id records nothing.
func TestRecordDeviceContactBlank(t *testing.T) {
	st := deviceTestStore(t)
	if _, err := recordDeviceContact(st, "", "alice@hermex.test", "iPhone", "ua", "14.1", 1000); err != nil {
		t.Fatalf("blank record: %v", err)
	}
	m, _ := loadDevices(st)
	if len(m.Devices) != 0 {
		t.Errorf("blank device id recorded %d devices, want 0", len(m.Devices))
	}
}

// TestDevicesMerge proves Devices merges recorded metadata with the live synced-
// folder count from the sync state, surfaces a device present in only one source,
// and returns the list sorted by device id.
func TestDevicesMerge(t *testing.T) {
	st := deviceTestStore(t)

	// dev-b has metadata and sync state; dev-c has only metadata.
	if _, err := recordDeviceContact(st, "dev-b", "alice@hermex.test", "Android", "ua-b", "14.1", 5000); err != nil {
		t.Fatalf("record dev-b: %v", err)
	}
	if _, err := recordDeviceContact(st, "dev-c", "alice@hermex.test", "iPhone", "ua-c", "14.1", 6000); err != nil {
		t.Fatalf("record dev-c: %v", err)
	}
	// dev-a has only sync state (1 collection); dev-b has 2 collections.
	state, err := loadState(st)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	state.device("dev-a").collection("1").SyncKey = "1"
	state.device("dev-b").collection("1").SyncKey = "1"
	state.device("dev-b").collection("2").SyncKey = "1"
	if err := saveState(st, state); err != nil {
		t.Fatalf("save state: %v", err)
	}

	devs, err := Devices(st)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("Devices returned %d, want 3 (dev-a/dev-b/dev-c)", len(devs))
	}
	if devs[0].DeviceID != "dev-a" || devs[1].DeviceID != "dev-b" || devs[2].DeviceID != "dev-c" {
		t.Fatalf("Devices not sorted by id: %s/%s/%s", devs[0].DeviceID, devs[1].DeviceID, devs[2].DeviceID)
	}
	if devs[0].FoldersSynced != 1 || devs[0].DeviceType != "" {
		t.Errorf("dev-a (sync-only) = %+v, want 1 folder and no metadata", devs[0])
	}
	if devs[1].FoldersSynced != 2 || devs[1].DeviceType != "Android" || devs[1].UserAgent != "ua-b" ||
		devs[1].LastSync != 5000 || devs[1].WipeStatus != WipeStatusOK {
		t.Errorf("dev-b (merged) = %+v, want 2 folders and Android metadata", devs[1])
	}
	if devs[2].FoldersSynced != 0 || devs[2].DeviceType != "iPhone" {
		t.Errorf("dev-c (metadata-only) = %+v, want metadata and 0 folders", devs[2])
	}
}
