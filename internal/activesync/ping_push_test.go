package activesync

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// fakePingWaker is a controllable wake source: Register hands back a channel the
// test fires to simulate a push event for the registered mailbox.
type fakePingWaker struct{ ch chan struct{} }

func (f *fakePingWaker) Register(string) (<-chan struct{}, func()) { return f.ch, func() {} }
func (f *fakePingWaker) fire() {
	select {
	case f.ch <- struct{}{}:
	default:
	}
}

// pingOutcome is one held Ping's reply, or the error that stopped it.
type pingOutcome struct {
	status string
	err    error
}

// heldPing starts a server with a controllable waker over a mailbox whose Inbox
// the device has synced, then starts a 60s Ping in the background. It returns
// the mailbox, the waker, and the channel that carries the Ping's outcome.
func heldPing(t *testing.T) (string, *fakePingWaker, <-chan pingOutcome) {
	t.Helper()
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
	t.Cleanup(ts.Close)

	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))

	out := make(chan pingOutcome, 1)
	go func() {
		_, root, err := postCommandErr(ts, "Ping", pingReq("60"))
		if err != nil {
			out <- pingOutcome{err: err}
			return
		}
		out <- pingOutcome{status: root.ChildText(wbxml.PGStatus)}
	}()
	time.Sleep(200 * time.Millisecond) // let Ping begin and register its wake
	return dir, waker, out
}

// TestPingWakesViaPush proves the rewired Ping loop returns on a push wake well
// before its poll cadence: a delivery lands during a held Ping on a quiet mailbox,
// and the wake makes the next poll fire at once (Status 2) rather than waiting out
// the 5s pingPoll. Without the wake arm the change would only be seen at the next
// cadence tick, so a sub-2s return proves the push path, not the poll.
func TestPingWakesViaPush(t *testing.T) {
	dir, waker, out := heldPing(t)
	seedInbox(t, dir, 1) // a delivery during the held Ping
	waker.fire()         // the push wake that should make Ping re-poll at once

	select {
	case o := <-out:
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.status != "2" {
			t.Errorf("Ping status = %q, want 2 (woken by push)", o.status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ping did not return within 2s (pingPoll cadence is 5s, so the push wake failed)")
	}
}

// TestPingHoldsOnAWakeWithoutChange proves a wake is a hint, not a verdict: the
// wake bus says only that something in the mailbox may have moved, and Ping runs
// its own diff ([MS-ASCMD] Ping Status 2 means changes occurred in a watched
// folder). A wake with no change must not end the hold.
func TestPingHoldsOnAWakeWithoutChange(t *testing.T) {
	_, waker, out := heldPing(t)
	waker.fire()

	select {
	case o := <-out:
		t.Errorf("Ping returned (status %q, err %v) on a wake that changed nothing", o.status, o.err)
	case <-time.After(1500 * time.Millisecond):
	}
}
