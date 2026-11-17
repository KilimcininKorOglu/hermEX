package activesync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// primeDeviceHierarchy sends one FolderSync prime as the named device.
func primeDeviceHierarchy(ts *httptest.Server, deviceID string) error {
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=FolderSync&User=" + testUser + "&DeviceId=" + deviceID + "&DeviceType=iPhone"
	body := wbxml.Marshal(wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// TestConcurrentDevicesKeepTheirState proves two devices of one mailbox that
// sync at the same time each keep their own state. The mailbox holds the state
// of every device in one stored value, so a command that reads it, changes its
// own device and writes the whole value back must not erase what another
// device's command wrote in between: a lost write makes that device re-prime
// its whole hierarchy.
func TestConcurrentDevicesKeepTheirState(t *testing.T) {
	ts, dir := seededServer(t)
	const devices = 8
	var wg sync.WaitGroup
	errs := make(chan error, devices)
	for i := range devices {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- primeDeviceHierarchy(ts, id)
		}("dev" + strconv.Itoa(i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		mustNoErr(t, "prime a device", err)
	}

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	state, err := loadState(st)
	mustNoErr(t, "load the state", err)
	for i := range devices {
		id := "dev" + strconv.Itoa(i)
		if d := state.Devices[id]; d == nil || d.HierarchyKey == "" {
			t.Errorf("device %s lost its hierarchy key", id)
		}
	}
	meta, err := loadDevices(st)
	mustNoErr(t, "load the device records", err)
	wantEq(t, "recorded devices", len(meta.Devices), devices)
}

// TestQueuedWipeSurvivesOtherDevicesContacts proves an administrator's remote
// wipe, written from the admin panel while other devices of the mailbox keep
// syncing, is not erased by one of those devices recording its contact. A lost
// write here would silently cancel the wipe.
func TestQueuedWipeSurvivesOtherDevicesContacts(t *testing.T) {
	dir := t.TempDir()
	open := func() *objectstore.Store {
		st, err := objectstore.Open(dir)
		mustNoErr(t, "open the store", err)
		t.Cleanup(func() { st.Close() })
		return st
	}
	daemon, panel := open(), open()
	mustRecordContact(t, daemon, "lost", "ua", 1)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for n := range 20 {
				if _, err := recordDeviceContact(daemon, id, testUser, "iPhone", "ua", "16.1", int64(n)); err != nil {
					t.Error(err)
				}
			}
		}("busy" + strconv.Itoa(i))
	}
	mustNoErr(t, "queue the wipe", RequestWipe(panel, "lost", false))
	wg.Wait()
	wantDeviceWipe(t, daemon, "after the other devices' contacts", "lost", WipeStatusPending)
}

// inboxSync is a Sync request body naming the Inbox at key.
func inboxSync(key string) *wbxml.Node {
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection,
		wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, inboxID()))))
}

// TestSyncCommitKeepsWhatOthersWrote proves a Sync writes back only the
// collections it ran, against the state as stored when it commits: another
// device's write made while the Sync ran survives, and an administrator's resync
// made meanwhile is not undone by the Sync re-creating the device.
func TestSyncCommitKeepsWhatOthersWrote(t *testing.T) {
	st := deviceTestStore(t)
	collections := inboxSync("0").Child(wbxml.ASCollections)

	// dev1 loads its state and runs a Sync of the Inbox.
	state, err := loadState(st)
	mustNoErr(t, "load the state", err)
	dev := state.device("dev1")
	before := collectionKeys(dev, collections)
	dev.collection(inboxID()).SyncKey = "1"

	// Meanwhile dev2 primes its hierarchy.
	_, ok, err := advanceHierarchy(st, "dev2", "")
	mustNoErr(t, "prime dev2", err)
	wantEq(t, "dev2's prime applied", ok, true)

	mustNoErr(t, "commit dev1's Sync", commitCollections(st, "dev1", dev, before))
	state, err = loadState(st)
	mustNoErr(t, "reload the state", err)
	wantEq(t, "dev1's Inbox key", state.device("dev1").collection(inboxID()).SyncKey, "1")
	wantEq(t, "dev2's hierarchy key", state.device("dev2").HierarchyKey, "1")

	// dev1 runs another Sync; the administrator resyncs dev1 before it commits.
	state, err = loadState(st)
	mustNoErr(t, "load the state", err)
	dev = state.device("dev1")
	before = collectionKeys(dev, inboxSync("1").Child(wbxml.ASCollections))
	dev.collection(inboxID()).SyncKey = "2"
	mustNoErr(t, "resync dev1", ResyncDevice(st, "dev1"))
	mustNoErr(t, "commit dev1's second Sync", commitCollections(st, "dev1", dev, before))
	state, err = loadState(st)
	mustNoErr(t, "reload the state", err)
	if d := state.Devices["dev1"]; d != nil && d.Collections[inboxID()] != nil {
		t.Errorf("the Sync undid the resync: dev1's Inbox key is %q", d.Collections[inboxID()].SyncKey)
	}
}
