package activesync

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// hangingSyncReq builds a Sync carrying a HeartbeatInterval (seconds) and one
// collection at the given key with no client commands — a pure "tell me when
// something changes" request.
func hangingSyncReq(key, hbSeconds string) *wbxml.Node {
	return wbxml.Elem(wbxml.ASSync,
		wbxml.Str(wbxml.ASHeartbeatInt, hbSeconds),
		wbxml.Elem(wbxml.ASCollections,
			wbxml.Elem(wbxml.ASCollection,
				wbxml.Str(wbxml.ASSyncKey, key),
				wbxml.Str(wbxml.ASCollectionID, inboxID()))))
}

// syncRespHasAdd reports whether a Sync response carries an Add command.
func syncRespHasAdd(root *wbxml.Node) bool {
	cols := root.Child(wbxml.ASCollections)
	if cols == nil {
		return false
	}
	coll := cols.Child(wbxml.ASCollection)
	if coll == nil {
		return false
	}
	cmds := coll.Child(wbxml.ASCommands)
	return cmds != nil && cmds.Child(wbxml.ASAdd) != nil
}

// TestSyncHoldWakesViaPush proves a hanging Sync returns on a push wake well before
// its heartbeat: a delivery during a held Sync on a quiet mailbox wakes it and the
// response carries the new message as an Add, where without the wake arm it would
// wait the 30s fallback cadence. A sub-2s return proves the push path.
func TestSyncHoldWakesViaPush(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	waker := &fakePingWaker{ch: make(chan struct{}, 1)}
	srv.waker = waker
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Establish the device snapshot on a quiet mailbox (sync key advances to 2).
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))

	done := make(chan *wbxml.Node, 1)
	go func() {
		_, root := postCommand(t, ts, "Sync", hangingSyncReq("2", "60"))
		done <- root
	}()
	time.Sleep(200 * time.Millisecond) // let the hold begin and register

	seedInbox(t, dir, 1) // a delivery during the held Sync
	waker.fire()         // the push wake

	select {
	case root := <-done:
		if !syncRespHasAdd(root) {
			t.Errorf("hanging Sync did not return the new message as an Add")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hanging Sync did not wake via push within 2s (fallback cadence is 30s)")
	}
}

// TestHoldForSyncTimesOut proves the hold returns false (an empty response) when the
// heartbeat expires on a quiet mailbox with no wake.
func TestHoldForSyncTimesOut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test") // no waker → only cadence + deadline
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadState(st2)
	if err != nil {
		t.Fatal(err)
	}
	dev := state.device("dev1")
	st2.Close()

	collections := hangingSyncReq("2", "60").Child(wbxml.ASCollections)
	if srv.holdForSync(context.Background(), dir, dev, collections, 300*time.Millisecond) {
		t.Error("holdForSync returned true on a quiet mailbox; want false (timeout → empty response)")
	}
}

// TestSyncHoldOutOfRange proves an out-of-range HeartbeatInterval yields Status 14
// with the nearest acceptable bound in a Limit element.
func TestSyncHoldOutOfRange(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "Sync", hangingSyncReq("1", "10")) // 10s < 60s minimum
	if s := root.ChildText(wbxml.ASStatus); s != strconv.Itoa(syncStatusWaitInterval) {
		t.Errorf("Status = %q, want %d (invalid wait/heartbeat)", s, syncStatusWaitInterval)
	}
	if lim := root.ChildText(wbxml.ASLimit); lim != "60" {
		t.Errorf("Limit = %q, want 60 (the minimum heartbeat)", lim)
	}
}
