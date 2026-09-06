package activesync

import (
	"testing"

	"hermex/internal/objectstore"
)

// deviceTestStore opens a fresh per-test mailbox store.
func deviceTestStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, "open store", err)
	t.Cleanup(func() { st.Close() })
	return st
}

// mustRecordContact stamps one device contact.
func mustRecordContact(t *testing.T, st *objectstore.Store, id, agent string, when int64) {
	t.Helper()
	_, err := recordDeviceContact(st, id, "alice@hermex.test", "iPhone", agent, "14.1", when)
	mustNoErr(t, "record the contact of "+id, err)
}

// mustDevice reads one device's recorded metadata, requiring it to be there.
func mustDevice(t *testing.T, st *objectstore.Store, id string) *deviceMeta {
	t.Helper()
	m, err := loadDevices(st)
	mustNoErr(t, "load the devices", err)
	d := m.Devices[id]
	if d == nil {
		t.Fatalf("device %q is not recorded", id)
	}
	return d
}

// mustAdvanceWipe runs one Provision wipe exchange and returns what it emits.
func mustAdvanceWipe(t *testing.T, st *objectstore.Store, id string, acked bool) int {
	t.Helper()
	emit, err := advanceProvisionWipe(st, id, acked)
	mustNoErr(t, "advance the wipe of "+id, err)
	return emit
}

// mustDevices lists the merged device rows.
func mustDevices(t *testing.T, st *objectstore.Store) []DeviceInfo {
	t.Helper()
	devs, err := Devices(st)
	mustNoErr(t, "list the devices", err)
	return devs
}

// TestRecordDeviceContact proves the first contact stamps firstSync and an OK
// wipe status, and a later contact refreshes lastSync and the live attributes
// without resetting firstSync or clearing an outstanding wipe status, otherwise
// every subsequent sync would silently cancel an administrator's pending wipe.
func TestRecordDeviceContact(t *testing.T) {
	st := deviceTestStore(t)

	mustRecordContact(t, st, "dev1", "Apple-iPhone/1", 1000)
	d := mustDevice(t, st, "dev1")
	wantEq(t, "first sync", d.FirstSync, int64(1000))
	wantEq(t, "last sync", d.LastSync, int64(1000))
	wantEq(t, "wipe status", d.WipeStatus, WipeStatusOK)
	wantEq(t, "device type", d.DeviceType, "iPhone")
	wantEq(t, "user agent", d.UserAgent, "Apple-iPhone/1")
	wantEq(t, "protocol version", d.ASVersion, "14.1")
	wantEq(t, "device user", d.DeviceUser, "alice@hermex.test")

	// Simulate an administrator-requested wipe, then a later contact.
	setDeviceWipe(t, st, "dev1", WipeStatusPending)
	mustRecordContact(t, st, "dev1", "Apple-iPhone/2", 2000)
	d = mustDevice(t, st, "dev1")
	wantEq(t, "first sync after the second contact", d.FirstSync, int64(1000))
	wantEq(t, "last sync after the second contact", d.LastSync, int64(2000))
	wantEq(t, "wipe status after the second contact", d.WipeStatus, WipeStatusPending)
	wantEq(t, "user agent after the second contact", d.UserAgent, "Apple-iPhone/2")
}

// setDeviceWipe forces a device's stored remote-wipe status.
func setDeviceWipe(t *testing.T, st *objectstore.Store, id string, status int) {
	t.Helper()
	m, err := loadDevices(st)
	mustNoErr(t, "load the devices", err)
	m.device(id).WipeStatus = status
	mustNoErr(t, "save the devices", saveDevices(st, m))
}

// wantDeviceWipe checks a device's stored remote-wipe status.
func wantDeviceWipe(t *testing.T, st *objectstore.Store, what, id string, want int) {
	t.Helper()
	m, err := loadDevices(st)
	mustNoErr(t, "load the devices", err)
	status := WipeStatusUnknown
	if d := m.Devices[id]; d != nil {
		status = d.WipeStatus
	}
	wantEq(t, "the wipe status "+what, status, want)
}

// TestAdvanceProvisionWipe proves the remote-wipe lifecycle across Provision
// exchanges: a device with no outstanding wipe emits nothing (so a normal
// Provision never carries a spurious directive), a pending wipe emits the wipe
// element and moves to requested, an acknowledgement moves it to wiped, and an
// account-only wipe takes the account path.
func TestAdvanceProvisionWipe(t *testing.T) {
	st := deviceTestStore(t)

	mustRecordContact(t, st, "dev1", "ua", 1000)
	wantEq(t, "what a device with no wipe emits", mustAdvanceWipe(t, st, "dev1", false), wipeEmitNone)

	setDeviceWipe(t, st, "dev1", WipeStatusPending)
	wantEq(t, "what a pending wipe emits", mustAdvanceWipe(t, st, "dev1", false), wipeEmitFull)
	wantDeviceWipe(t, st, "after the delivery", "dev1", WipeStatusRequested)
	wantEq(t, "what an acked wipe emits", mustAdvanceWipe(t, st, "dev1", true), wipeEmitFull)
	wantDeviceWipe(t, st, "after the acknowledgement", "dev1", WipeStatusWiped)

	// Wiped is terminal: a further exchange emits nothing and stays wiped, so a
	// device that reconnects is not wiped again in a loop.
	wantEq(t, "what a wiped device emits", mustAdvanceWipe(t, st, "dev1", false), wipeEmitNone)
	wantDeviceWipe(t, st, "after a further exchange", "dev1", WipeStatusWiped)

	setDeviceWipe(t, st, "dev1", WipeStatusAccountPending)
	wantEq(t, "what an account-only wipe emits", mustAdvanceWipe(t, st, "dev1", false), wipeEmitAccount)
	wantDeviceWipe(t, st, "after the account delivery", "dev1", WipeStatusAccountRequested)
}

// TestDeviceMutations proves the management actions on device state: resync
// clears a device's sync state but keeps it listed, a queued wipe survives a
// later contact (so it actually reaches the device) and can be cancelled, an
// account-only wipe takes the account-pending status, and delete removes the
// device entirely.
func TestDeviceMutations(t *testing.T) {
	st := deviceTestStore(t)

	mustRecordContact(t, st, "dev1", "ua", 1000)
	seedSyncedCollections(t, st, "dev1", "1", "2")

	mustNoErr(t, "resync the device", ResyncDevice(st, "dev1"))
	devs := mustDevices(t, st)
	if len(devs) != 1 {
		t.Fatalf("after the resync devices = %+v, want dev1 still listed", devs)
	}
	wantEq(t, "folders synced after the resync", devs[0].FoldersSynced, 0)

	// A contact must not cancel a queued wipe, or it would never reach the device.
	mustNoErr(t, "queue the wipe", RequestWipe(st, "dev1", false))
	mustRecordContact(t, st, "dev1", "ua", 2000)
	wantDeviceWipe(t, st, "after a contact", "dev1", WipeStatusPending)

	mustNoErr(t, "cancel the wipe", CancelWipe(st, "dev1"))
	wantDeviceWipe(t, st, "after the cancel", "dev1", WipeStatusOK)

	mustNoErr(t, "queue the account wipe", RequestWipe(st, "dev1", true))
	wantDeviceWipe(t, st, "after the account wipe", "dev1", WipeStatusAccountPending)

	mustNoErr(t, "delete the device", DeleteDevice(st, "dev1"))
	wantEq(t, "devices after the delete", len(mustDevices(t, st)), 0)
}

// seedSyncedCollections gives a device a sync state over the named collections.
func seedSyncedCollections(t *testing.T, st *objectstore.Store, deviceID string, collIDs ...string) {
	t.Helper()
	state, err := loadState(st)
	mustNoErr(t, "load the sync state", err)
	for _, id := range collIDs {
		state.device(deviceID).collection(id).SyncKey = "1"
	}
	mustNoErr(t, "save the sync state", saveState(st, state))
}

// TestRecordDeviceContactBlank proves a request with no device id records nothing.
func TestRecordDeviceContactBlank(t *testing.T) {
	st := deviceTestStore(t)
	mustRecordContact(t, st, "", "ua", 1000)
	m, err := loadDevices(st)
	mustNoErr(t, "load the devices", err)
	wantEq(t, "devices recorded for a blank device id", len(m.Devices), 0)
}

// TestDevicesMerge proves Devices merges recorded metadata with the live synced-
// folder count from the sync state, surfaces a device present in only one source,
// and returns the list sorted by device id.
func TestDevicesMerge(t *testing.T) {
	st := deviceTestStore(t)

	// dev-b has metadata and sync state; dev-c has only metadata.
	_, err := recordDeviceContact(st, "dev-b", "alice@hermex.test", "Android", "ua-b", "14.1", 5000)
	mustNoErr(t, "record dev-b", err)
	mustRecordContact(t, st, "dev-c", "ua-c", 6000)
	// dev-a has only sync state (1 collection); dev-b has 2 collections.
	seedSyncedCollections(t, st, "dev-a", "1")
	seedSyncedCollections(t, st, "dev-b", "1", "2")

	devs := mustDevices(t, st)
	if len(devs) != 3 {
		t.Fatalf("Devices returned %d, want 3 (dev-a/dev-b/dev-c)", len(devs))
	}
	wantEq(t, "the first device id", devs[0].DeviceID, "dev-a")
	wantEq(t, "the second device id", devs[1].DeviceID, "dev-b")
	wantEq(t, "the third device id", devs[2].DeviceID, "dev-c")

	wantEq(t, "folders synced by the sync-only device", devs[0].FoldersSynced, 1)
	wantEq(t, "the device type of the sync-only device", devs[0].DeviceType, "")

	wantEq(t, "folders synced by the merged device", devs[1].FoldersSynced, 2)
	wantEq(t, "the device type of the merged device", devs[1].DeviceType, "Android")
	wantEq(t, "the user agent of the merged device", devs[1].UserAgent, "ua-b")
	wantEq(t, "the last sync of the merged device", devs[1].LastSync, int64(5000))
	wantEq(t, "the wipe status of the merged device", devs[1].WipeStatus, WipeStatusOK)

	wantEq(t, "folders synced by the metadata-only device", devs[2].FoldersSynced, 0)
	wantEq(t, "the device type of the metadata-only device", devs[2].DeviceType, "iPhone")
}
