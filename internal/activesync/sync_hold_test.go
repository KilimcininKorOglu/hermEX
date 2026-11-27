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
// collection at the given key with no client commands, a pure "tell me when
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
	mustNoErr(t, "close the store", st.Close())
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

	type outcome struct {
		root *wbxml.Node
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		// postCommandErr, not postCommand: t.Fatal must not run off the test goroutine.
		_, root, err := postCommandErr(context.Background(), ts, "Sync", hangingSyncReq("2", "60"))
		done <- outcome{root, err}
	}()
	time.Sleep(200 * time.Millisecond) // let the hold begin and register

	seedInbox(t, dir, 1) // a delivery during the held Sync
	waker.fire()         // the push wake

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatal(o.err)
		}
		if !syncRespHasAdd(o.root) {
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
	mustNoErr(t, "close the store", st.Close())
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
	mustNoErr(t, "close the second store", st2.Close())

	collections := hangingSyncReq("2", "60").Child(wbxml.ASCollections)
	start := time.Now()
	if srv.holdForSync(context.Background(), dir, dev, collections, 300*time.Millisecond) {
		t.Error("holdForSync returned true on a quiet mailbox; want false (timeout → empty response)")
	}
	// A false that came back at once would be no hold at all.
	if waited := time.Since(start); waited < 300*time.Millisecond {
		t.Errorf("holdForSync returned after %v, before its 300ms deadline", waited)
	}
}

// TestSyncHoldOutOfRange proves an out-of-range HeartbeatInterval yields Status 14
// with the nearest acceptable bound in a Limit element. [MS-ASCMD] Sync
// HeartbeatInterval: the valid range is 60 through 3540 seconds.
func TestSyncHoldOutOfRange(t *testing.T) {
	for _, tc := range []struct{ heartbeat, limit string }{
		{"10", "60"},
		{"-5", "60"},
		{"3541", "3540"},
		{"99999999999999999999", "3540"},
		// 18446744075 seconds wraps a nanosecond Duration to about 1.3s.
		{"18446744075", "3540"},
		{"abc", "60"},
	} {
		ts, _ := seededServer(t)
		// A server that accepts the value holds the request for its heartbeat, so the
		// reply is awaited with a bound rather than for up to an hour.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel) // runs before the server's Close, which waits for the held request
		reply := make(chan *wbxml.Node, 1)
		errs := make(chan error, 1)
		go func() {
			_, root, err := postCommandErr(ctx, ts, "Sync", hangingSyncReq("1", tc.heartbeat))
			if err != nil {
				errs <- err
				return
			}
			reply <- root
		}()
		var root *wbxml.Node
		select {
		case root = <-reply:
		case err := <-errs:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatalf("heartbeat %s was held instead of refused", tc.heartbeat)
		}
		if s := root.ChildText(wbxml.ASStatus); s != strconv.Itoa(syncStatusWaitInterval) {
			t.Errorf("heartbeat %s: Status = %q, want %d (invalid wait/heartbeat)", tc.heartbeat, s, syncStatusWaitInterval)
		}
		if lim := root.ChildText(wbxml.ASLimit); lim != tc.limit {
			t.Errorf("heartbeat %s: Limit = %q, want %s", tc.heartbeat, lim, tc.limit)
		}
	}
}

// TestSyncHoldAcceptsTheRangeEdges proves 60 and 3540, the ends of the
// [MS-ASCMD] HeartbeatInterval range, are accepted: the request is held, not
// refused at once with Status 14.
func TestSyncHoldAcceptsTheRangeEdges(t *testing.T) {
	for _, hb := range []string{"60", "3540"} {
		ts, _ := seededServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel) // ends the held request before the server's Close waits for it
		reply := make(chan *wbxml.Node, 1)
		errs := make(chan error, 1)
		go func() {
			_, root, err := postCommandErr(ctx, ts, "Sync", hangingSyncReq("1", hb))
			if err != nil {
				errs <- err
				return
			}
			reply <- root
		}()
		// Within the second nothing cancels ctx, so an error here is the server
		// answering the request at once (an HTTP failure), not holding it.
		select {
		case root := <-reply:
			t.Errorf("heartbeat %s answered at once (Status %q), want it held", hb, root.ChildText(wbxml.ASStatus))
		case err := <-errs:
			t.Errorf("heartbeat %s failed at once (%v), want it held", hb, err)
		case <-time.After(time.Second):
		}
	}
}
