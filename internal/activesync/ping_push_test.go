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

// TestPingWakesViaPush proves the rewired Ping loop returns on a push wake well
// before its poll cadence: a delivery lands during a held Ping on a quiet mailbox,
// and the wake makes the next poll fire at once (Status 2) rather than waiting out
// the 5s pingPoll. Without the wake arm the change would only be seen at the next
// cadence tick, so a sub-2s return proves the push path, not the poll.
func TestPingWakesViaPush(t *testing.T) {
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

	// Establish the device snapshot on a quiet mailbox.
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))

	status := make(chan string, 1)
	go func() {
		_, root := postCommand(t, ts, "Ping", pingReq("30"))
		status <- root.ChildText(wbxml.PGStatus)
	}()
	time.Sleep(200 * time.Millisecond) // let Ping begin and register its wake

	seedInbox(t, dir, 1) // a delivery during the held Ping
	waker.fire()         // the push wake that should make Ping re-poll at once

	select {
	case s := <-status:
		if s != "2" {
			t.Errorf("Ping status = %q, want 2 (woken by push)", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ping did not return within 2s (pingPoll cadence is 5s, so the push wake failed)")
	}
}
