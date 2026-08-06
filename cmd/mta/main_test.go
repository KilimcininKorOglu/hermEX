package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// TestSweepOutboxesDeliversDueScheduledSend is a light integration test of the
// send-later worker's one-pass sweep: a past-due message in one mailbox's Outbox
// is delivered to a second mailbox through the real local-delivery path, filed to
// the sender's Sent, and removed from the Outbox.
func TestSweepOutboxesDeliversDueScheduledSend(t *testing.T) {
	root := t.TempDir()
	aliceDir := filepath.Join(root, "alice")
	bobDir := filepath.Join(root, "bob")

	// Queue a past-due scheduled send in alice's Outbox, addressed to bob.
	alice, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: scheduled\r\n\r\nbody\r\n"
	info, err := alice.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(time.Now().Add(-time.Minute))},
	}); err != nil {
		t.Fatal(err)
	}
	alice.Close()
	if bob, err := objectstore.Open(bobDir); err != nil { // provision bob's mailbox
		t.Fatal(err)
	} else {
		bob.Close()
	}

	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: aliceDir},
		"bob@hermex.test":   {MailboxPath: bobDir},
	}
	deliver := func(recipients []string, raw []byte, when time.Time) ([]string, error) {
		return mta.Deliver(accounts, senderOf(raw), recipients, raw, when)
	}

	sweepOutboxes(context.Background(), accounts, deliver, nil, nil)

	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("alice Outbox has %d after sweep, want 0 (released)", n)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("alice Sent has %d, want 1", n)
	}
	if n := folderCount(t, bobDir, int64(mapi.PrivateFIDInbox)); n != 1 {
		t.Errorf("bob Inbox has %d, want 1 (the scheduled send should have been delivered)", n)
	}
}

func folderCount(t *testing.T, path string, fid int64) int {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestGuardedSweepRefusalLeavesTheOutboxAlone proves a second MTA instance, told
// another one is already sweeping, does not touch any mailbox. Running anyway
// would re-deliver a scheduled message in the window between its delivery and its
// removal from the Outbox, which is the race the lock exists to close.
func TestGuardedSweepRefusalLeavesTheOutboxAlone(t *testing.T) {
	root := t.TempDir()
	aliceDir := filepath.Join(root, "alice")
	alice, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: scheduled\r\n\r\nbody\r\n"
	info, err := alice.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(time.Now().Add(-time.Minute))},
	}); err != nil {
		t.Fatal(err)
	}
	alice.Close()

	accounts := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: aliceDir}}
	delivered := 0
	deliver := func(recipients []string, raw []byte, when time.Time) ([]string, error) {
		delivered++
		return nil, nil
	}

	guardedSweep(context.Background(), accounts, deliver, nil, func() (func(), bool) { return nil, false }, nil)
	if delivered != 0 {
		t.Errorf("a refused sweep delivered %d message(s), want none", delivered)
	}
	if n := folderCount(t, aliceDir, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("alice Outbox has %d after a refused sweep, want the message untouched", n)
	}

	// Permitted: the sweep runs and hands the permission back.
	released := 0
	guardedSweep(context.Background(), accounts, deliver, nil, func() (func(), bool) { return func() { released++ }, true }, nil)
	if delivered != 1 {
		t.Errorf("a permitted sweep delivered %d message(s), want 1", delivered)
	}
	if released != 1 {
		t.Errorf("permission released %d times, want 1", released)
	}
}

// TestSweepOutboxesStopsOnShutdown proves the send-later sweep abandons its walk
// when the daemon is shutting down. The sweep opens one mailbox store after
// another, so without this it would keep working through every remaining mailbox
// after the signal, and on a large deployment outlast the shutdown deadline that
// is supposed to let it drain.
func TestSweepOutboxesStopsOnShutdown(t *testing.T) {
	root := t.TempDir()
	accounts := directory.StaticAccounts{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		dir := filepath.Join(root, name)
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		raw := "From: " + name + "@hermex.test\r\nTo: sink@hermex.test\r\nSubject: s\r\n\r\nbody\r\n"
		info, err := st.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetMessageProperties(info.ID, mapi.PropertyValues{
			{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(time.Now().Add(-time.Minute))},
		}); err != nil {
			t.Fatal(err)
		}
		st.Close()
		accounts[name+"@hermex.test"] = directory.Account{MailboxPath: dir}
	}

	// A mailbox that has never been opened. Opening a store provisions it, so
	// whether this directory exists afterwards says whether the sweep kept walking
	// past the shutdown signal.
	unvisited := filepath.Join(root, "unvisited")
	accounts["z@hermex.test"] = directory.Account{MailboxPath: unvisited}

	ctx, cancel := context.WithCancel(context.Background())
	sends := 0
	deliver := func([]string, []byte, time.Time) ([]string, error) {
		sends++
		// The shutdown signal arrives during the first mailbox's release.
		cancel()
		return nil, nil
	}
	sweepOutboxes(ctx, accounts, deliver, nil, nil)

	if sends != 1 {
		t.Errorf("the sweep released %d messages after the shutdown signal, want 1", sends)
	}
	if _, err := os.Stat(unvisited); err == nil {
		t.Error("the sweep opened a further mailbox after the shutdown signal; on a large deployment it would outlast the drain deadline")
	}
	// The rest are untouched, still scheduled for the next start.
	waiting := 0
	for name := range accounts {
		if accounts[name].MailboxPath == unvisited {
			continue
		}
		st, err := objectstore.Open(accounts[name].MailboxPath)
		if err != nil {
			t.Fatal(err)
		}
		msgs, err := st.ListMessages(int64(mapi.PrivateFIDOutbox))
		st.Close()
		if err != nil {
			t.Fatal(err)
		}
		waiting += len(msgs)
	}
	if waiting != 4 {
		t.Errorf("%d messages are still scheduled, want 4 (only the in-flight one released)", waiting)
	}
}

// sweepSink records the events one send-later sweep emits.
type sweepSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *sweepSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *sweepSink) find(name string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// scheduleFor files a scheduled message in a mailbox's Outbox at the given send
// time and returns the mailbox path.
func scheduleFor(t *testing.T, root, name string, when time.Time) string {
	t.Helper()
	dir := filepath.Join(root, name)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: " + name + "@hermex.test\r\nTo: sink@hermex.test\r\nSubject: s\r\n\r\nbody\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDOutbox), []byte(raw), time.Unix(1, 0), objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(when)},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSweepEmitsQueueDepth proves each sweep reports what the queue looks like,
// not only what it did. The per-mailbox lines appear only when something happens,
// so a backlog that is merely growing, rather than failing, would otherwise leave
// no trace: nothing releases and nothing errors while the queue fills.
func TestSweepEmitsQueueDepth(t *testing.T) {
	root := t.TempDir()
	accounts := directory.StaticAccounts{
		"due@hermex.test":    {MailboxPath: scheduleFor(t, root, "due", time.Now().Add(-time.Minute))},
		"later@hermex.test":  {MailboxPath: scheduleFor(t, root, "later", time.Now().Add(time.Hour))},
		"later2@hermex.test": {MailboxPath: scheduleFor(t, root, "later2", time.Now().Add(time.Hour))},
	}
	sink := &sweepSink{}
	deliver := func([]string, []byte, time.Time) ([]string, error) { return nil, nil }

	sweepOutboxes(context.Background(), accounts, deliver, nil, logging.New(sink))

	e, ok := sink.find("sendlater.sweep")
	if !ok {
		t.Fatal("no sweep summary event; a growing backlog would be invisible")
	}
	if e.Fields["released"] != 1 {
		t.Errorf("released = %v, want 1", e.Fields["released"])
	}
	if e.Fields["waiting"] != 2 {
		t.Errorf("waiting = %v, want 2 (the queue depth after the pass)", e.Fields["waiting"])
	}
	if e.Fields["mailboxes"] != 3 {
		t.Errorf("mailboxes = %v, want 3", e.Fields["mailboxes"])
	}
	if e.Level != logging.LevelInfo {
		t.Errorf("level = %q, want info for a clean sweep", e.Level)
	}
}

// TestSweepReportsFailuresAndRetries proves a failing send is counted and shows up
// as retrying, and that the sweep summary rises to a warning. That is the signal
// an operator needs before a stuck message exhausts its budget and bounces.
func TestSweepReportsFailuresAndRetries(t *testing.T) {
	root := t.TempDir()
	accounts := directory.StaticAccounts{
		"stuck@hermex.test": {MailboxPath: scheduleFor(t, root, "stuck", time.Now().Add(-time.Minute))},
	}
	sink := &sweepSink{}
	failing := func([]string, []byte, time.Time) ([]string, error) {
		return nil, errors.New("recipient mailbox unavailable")
	}

	sweepOutboxes(context.Background(), accounts, failing, nil, logging.New(sink))

	e, ok := sink.find("sendlater.sweep")
	if !ok {
		t.Fatal("no sweep summary event")
	}
	if e.Fields["failed"] != 1 {
		t.Errorf("failed = %v, want 1", e.Fields["failed"])
	}
	if e.Fields["retrying"] != 1 {
		t.Errorf("retrying = %v, want 1", e.Fields["retrying"])
	}
	if e.Fields["released"] != 0 {
		t.Errorf("released = %v, want 0", e.Fields["released"])
	}
	if e.Level != logging.LevelWarn {
		t.Errorf("level = %q, want warn when a mailbox failed", e.Level)
	}
}
