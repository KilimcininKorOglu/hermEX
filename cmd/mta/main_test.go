package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// orderedMaildirs is a directory.MailboxLister with a fixed walk order. The
// static directory enumerates a map, so its order is random, and a test that
// depends on which mailbox the sweep reaches first cannot use it.
type orderedMaildirs struct{ paths []string }

func (o orderedMaildirs) Maildirs() ([]string, error) { return o.paths, nil }

// TestSweepOutboxesStopsOnShutdown proves the send-later sweep abandons its walk
// when the daemon is shutting down. The sweep opens one mailbox store after
// another, so without this it would keep working through every remaining mailbox
// after the signal, and on a large deployment outlast the shutdown deadline that
// is supposed to let it drain.
func TestSweepOutboxesStopsOnShutdown(t *testing.T) {
	root := t.TempDir()
	accounts := orderedMaildirs{}
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
		accounts.paths = append(accounts.paths, dir)
	}

	// A mailbox that has never been opened, listed LAST so the sweep can only
	// reach it by continuing past the signal. Opening a store provisions it, so
	// whether this directory exists afterwards says whether the sweep kept walking.
	unvisited := filepath.Join(root, "unvisited")
	accounts.paths = append(accounts.paths, unvisited)

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
	for _, path := range accounts.paths {
		if path == unvisited {
			continue
		}
		st, err := objectstore.Open(path)
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

// TestSlowMailboxDoesNotStarveTheRest is the starvation regression. The sweep
// walks mailboxes in order, so a single slow one used to consume the whole pass
// and every mailbox behind it waited for the next pass, and the next. Here the
// first mailbox spends longer than its budget and the second must still be served
// in the same pass.
func TestSlowMailboxDoesNotStarveTheRest(t *testing.T) {
	root := t.TempDir()
	slowDir := filepath.Join(root, "aaa-slow")
	fastDir := filepath.Join(root, "zzz-fast")
	// Several due messages in the slow mailbox, so the budget expires partway.
	for range 4 {
		scheduleInto(t, slowDir, "slow", time.Now().Add(-time.Minute))
	}
	scheduleInto(t, fastDir, "fast", time.Now().Add(-time.Minute))

	accounts := directory.StaticAccounts{
		"slow@hermex.test": {MailboxPath: slowDir},
		"fast@hermex.test": {MailboxPath: fastDir},
	}
	// A short budget keeps the test quick; the production value is the sweep
	// interval, and the behaviour under test is the same at any size.
	restore := perMailboxSweepBudget
	perMailboxSweepBudget = 100 * time.Millisecond
	t.Cleanup(func() { perMailboxSweepBudget = restore })

	sink := &sweepSink{}
	var mu sync.Mutex
	served := map[string]int{}
	deliver := func(_ []string, raw []byte, _ time.Time) ([]string, error) {
		mu.Lock()
		who := "fast"
		if strings.Contains(string(raw), "slow@hermex.test") {
			who = "slow"
			// Each slow release eats most of the budget, so the second one crosses it.
			time.Sleep(perMailboxSweepBudget / 2)
		}
		served[who]++
		mu.Unlock()
		return nil, nil
	}

	sweepOutboxes(context.Background(), accounts, deliver, nil, logging.New(sink))

	mu.Lock()
	defer mu.Unlock()
	if served["fast"] != 1 {
		t.Errorf("the fast mailbox was served %d times, want 1; the slow one starved it", served["fast"])
	}
	if served["slow"] >= 4 {
		t.Errorf("the slow mailbox released %d messages, want it cut off at its budget", served["slow"])
	}
	if _, ok := sink.find("sendlater.budget"); !ok {
		t.Error("no budget event; an operator could not see which mailbox is holding the sweep up")
	}
}

// scheduleInto files one past-due scheduled message into an existing or new
// mailbox directory.
func scheduleInto(t *testing.T, dir, name string, when time.Time) {
	t.Helper()
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
}

// ruleSink collects the events a rule hook emits.
type ruleSink struct{ events []logging.Event }

func (s *ruleSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestRuleHookRecordsBothFailures proves a delivery-time inbox rule that does not
// reach the wire leaves a record. Both paths are silent to the user (neither can
// fail the delivery that triggered the rule), so without the event an operator
// investigating "my forwarding rule did not fire" has nothing to look at.
func TestRuleHookRecordsBothFailures(t *testing.T) {
	// A cap of one recipient window, so the second call is over the cap.
	limiter := mta.NewOutboundLimiter()
	limiter.SetLimits(1, time.Hour)
	limiter.SetEnabled(true)

	sink := &ruleSink{}
	failing := func(string, []string, []byte, time.Time) error { return errors.New("spool is full") }
	hook := ruleHook("forward", limiter, failing, logging.New(sink))

	// First call is under the cap and reaches the spool, which refuses it.
	hook("alice@hermex.test", []string{"bob@example.com"}, []byte("raw"))
	// Second call is over the cap and never reaches the spool.
	hook("alice@hermex.test", []string{"bob@example.com"}, []byte("raw"))

	if len(sink.events) != 2 {
		t.Fatalf("hook emitted %d events, want one per failure", len(sink.events))
	}
	if e := sink.events[0]; e.Name != "rule.forward.enqueue" || e.Err != "spool is full" || e.User != "alice@hermex.test" {
		t.Errorf("enqueue failure = %s user=%q err=%q, want rule.forward.enqueue naming the owner and the error",
			e.Name, e.User, e.Err)
	}
	if e := sink.events[1]; e.Name != "rule.forward.deferred" || e.Level != logging.LevelWarn {
		t.Errorf("over-cap event = %s/%s, want warn/rule.forward.deferred", e.Level, e.Name)
	}
}

// TestRuleHookStaysSilentOnSuccess is the negative control: a forward that goes out
// is the normal case and must not log.
func TestRuleHookStaysSilentOnSuccess(t *testing.T) {
	sink := &ruleSink{}
	var sent int
	ok := func(string, []string, []byte, time.Time) error { sent++; return nil }
	hook := ruleHook("send", mta.NewOutboundLimiter(), ok, logging.New(sink))

	hook("alice@hermex.test", []string{"bob@example.com"}, []byte("raw"))

	if sent != 1 {
		t.Errorf("the hook enqueued %d times, want 1", sent)
	}
	if len(sink.events) != 0 {
		t.Errorf("a successful forward emitted %d events, want none: %+v", len(sink.events), sink.events)
	}
}

// TestDigestOnceRunsOnlyUnderTheLock proves the digest pass is guarded like the
// other two singleton sweeps in this daemon. Without the guard, two instances read
// the same pre-advance watermark and each mails a full digest, with its own set of
// valid one-click release links.
func TestDigestOnceRunsOnlyUnderTheLock(t *testing.T) {
	// An empty runner returns immediately without touching the directory, so this
	// exercises the guard alone.
	runner := &mta.DigestRunner{}

	released := false
	held := func() (func(), bool) { return func() { released = true }, true }
	if _, ran := digestOnce(runner, held); !ran {
		t.Error("the pass did not run while holding the lock")
	}
	if !released {
		t.Error("the pass kept the lock after finishing; the next tick would find it held")
	}

	// Refused: another instance is mid-pass, or the directory could not answer.
	refused := func() (func(), bool) { return nil, false }
	if _, ran := digestOnce(runner, refused); ran {
		t.Error("the pass ran without the lock")
	}
}
