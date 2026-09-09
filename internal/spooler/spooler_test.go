package spooler

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// releaseOutbox runs one Outbox sweep through the production ProcessDueOutboxStats
// and projects the released count the tests assert on, so the tests exercise the
// single production entry point rather than a wrapper that no production path calls.
func releaseOutbox(ctx context.Context, st *objectstore.Store, deliver DeliverFunc, onGiveUp GiveUpFunc, now time.Time) (int, error) {
	stats, err := ProcessDueOutboxStats(ctx, st, deliver, onGiveUp, now)
	return stats.Released, err
}

// openStore provisions a fresh, fully seeded mailbox (Outbox and Sent present).
func openStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// scheduleOutbox files a To/Cc/Bcc message in the Outbox and stamps it with a
// deferred-send time, the shape a send-later compose produces.
func scheduleOutbox(t *testing.T, st *objectstore.Store, when time.Time) {
	t.Helper()
	raw := "From: alice@hermex.test\r\n" +
		"To: to@example.com\r\n" +
		"Cc: cc@example.com\r\n" +
		"Bcc: bcc@example.com\r\n" +
		"Subject: scheduled\r\n" +
		"\r\n" +
		"scheduled body\r\n"
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

func count(t *testing.T, st *objectstore.Store, fid int64) int {
	t.Helper()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestProcessDueOutboxReleasesDueMessage checks the core release path: a past-due
// scheduled message is delivered to every recipient (To, Cc, and the blind Bcc),
// the delivered wire copy has the Bcc header stripped (the blind list must never
// reach the wire) while the Sent copy keeps it, and the Outbox is cleared.
func TestProcessDueOutboxReleasesDueMessage(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	var gotRcpts []string
	var gotRaw []byte
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		gotRcpts = slices.Clone(rcpts)
		gotRaw = slices.Clone(raw)
		return nil, nil
	}

	released, err := releaseOutbox(context.Background(), st, deliver, nil, time.Now())
	mustNoErr(t, err, "release the outbox")
	wantEq(t, released, 1, "released messages")

	// Every recipient, including the blind Bcc, must be delivered to.
	for _, want := range []string{"to@example.com", "cc@example.com", "bcc@example.com"} {
		wantEq(t, slices.Contains(gotRcpts, want), true, "the delivery reaches "+want)
	}
	// The delivered bytes carry To and Cc but never the blind Bcc address (which
	// appears only in the Bcc header, so its absence proves the header was cut).
	dw := string(gotRaw)
	wantContains(t, dw, "to@example.com", "the delivered copy keeps To")
	wantContains(t, dw, "cc@example.com", "the delivered copy keeps Cc")
	wantNotContains(t, dw, "bcc@example.com", "the delivered copy leaks the blind Bcc")

	// The Outbox is cleared and the Sent copy keeps the Bcc record.
	wantEq(t, count(t, st, int64(mapi.PrivateFIDOutbox)), 0, "Outbox messages after the release")
	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	mustNoErr(t, err, "list Sent")
	if len(sent) != 1 {
		t.Fatalf("Sent has %d, want 1", len(sent))
	}
	sentRaw, err := st.GetMessageRaw(int64(mapi.PrivateFIDSentItems), sent[0].UID)
	mustNoErr(t, err, "read the Sent copy")
	wantContains(t, string(sentRaw), "bcc@example.com", "the Sent copy keeps the Bcc record")
}

// TestProcessDueOutboxSkipsFutureMessage checks that a message whose deferred
// time has not yet come is left untouched and not delivered.
func TestProcessDueOutboxSkipsFutureMessage(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(time.Hour))

	called := false
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		called = true
		return nil, nil
	}
	released, err := releaseOutbox(context.Background(), st, deliver, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 || called {
		t.Errorf("a future message was released (released=%d, deliver called=%v)", released, called)
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("future message left the Outbox (count=%d, want 1)", n)
	}
}

// TestProcessDueOutboxKeepsOnDeliverError checks that a delivery failure leaves
// the message in the Outbox to retry and files nothing to Sent, and that the
// failure is reported.
func TestProcessDueOutboxKeepsOnDeliverError(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		return nil, errors.New("transport unavailable")
	}
	released, err := releaseOutbox(context.Background(), st, deliver, nil, time.Now())
	if released != 0 {
		t.Errorf("released %d on delivery failure, want 0", released)
	}
	if err == nil {
		t.Error("a delivery failure should be reported")
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("a failed message must stay in the Outbox (count=%d, want 1)", n)
	}
	if n := count(t, st, int64(mapi.PrivateFIDSentItems)); n != 0 {
		t.Errorf("a failed message must not be filed to Sent (count=%d, want 0)", n)
	}
}

// TestProcessDueOutboxNeverRedeliversWhenFilingFails is the regression that
// matters most: the message is delivered, filing the Sent copy then fails
// permanently. The message must leave the Outbox anyway. Holding it there would
// re-deliver the same mail to every recipient on every sweep, forever.
func TestProcessDueOutboxNeverRedeliversWhenFilingFails(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))
	// Make filing the Sent copy fail for good. The store refuses to remove a
	// built-in folder, so the failure is staged at the write itself.
	restore := fileToSent
	fileToSent = func(*objectstore.Store, []byte, time.Time) error {
		return errors.New("sent folder unavailable")
	}
	t.Cleanup(func() { fileToSent = restore })

	deliveries := 0
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		deliveries++
		return nil, nil
	}
	released, err := releaseOutbox(context.Background(), st, deliver, nil, time.Now())
	if released != 1 {
		t.Errorf("released %d, want 1: the mail did go out", released)
	}
	if err == nil {
		t.Error("the lost Sent copy must be reported")
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Fatalf("Outbox still holds %d message(s); it would re-deliver every sweep", n)
	}

	// A second sweep must not send the message again.
	if _, err := releaseOutbox(context.Background(), st, deliver, nil, time.Now()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if deliveries != 1 {
		t.Errorf("delivered %d times across two sweeps, want exactly 1", deliveries)
	}
}

// TestProcessDueOutboxGivesUpAfterMaxAttempts proves a message that can never be
// released stops retrying: after the attempt budget it moves back to Drafts (where
// a user-cancelled scheduled send also lands) with its deferred-send time gone, and
// the sender is told through the give-up hook.
func TestProcessDueOutboxGivesUpAfterMaxAttempts(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		return nil, errors.New("transport unavailable")
	}
	var gaveUp int
	var gotRecipients []string
	var gotRaw []byte
	onGiveUp := func(raw []byte, recipients []string, cause error) {
		gaveUp++
		gotRecipients = recipients
		gotRaw = raw
	}

	for i := 1; i < maxReleaseAttempts; i++ {
		if _, err := releaseOutbox(context.Background(), st, deliver, onGiveUp, time.Now()); err == nil {
			t.Fatalf("attempt %d: a delivery failure should be reported", i)
		}
		if gaveUp != 0 {
			t.Fatalf("gave up after %d attempts, want only after %d", i, maxReleaseAttempts)
		}
		if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
			t.Fatalf("attempt %d: message left the Outbox early (count=%d)", i, n)
		}
	}

	if _, err := releaseOutbox(context.Background(), st, deliver, onGiveUp, time.Now()); err == nil {
		t.Fatal("the final attempt should report the abandonment")
	}
	wantEq(t, gaveUp, 1, "give-up hook calls")
	wantEq(t, slices.Contains(gotRecipients, "to@example.com"), true, "the give-up reports the To recipient")
	wantEq(t, slices.Contains(gotRecipients, "bcc@example.com"), true, "the give-up reports the Bcc recipient")
	wantContains(t, string(gotRaw), "Subject: scheduled", "the give-up hook receives the abandoned message")
	wantEq(t, count(t, st, int64(mapi.PrivateFIDOutbox)), 0, "Outbox messages after giving up")

	drafts, err := st.ListMessages(int64(mapi.PrivateFIDDraft))
	mustNoErr(t, err, "list Drafts")
	if len(drafts) != 1 {
		t.Fatalf("Drafts holds %d message(s), want the abandoned one", len(drafts))
	}
	props, err := st.GetMessageProperties(drafts[0].ID, mapi.PrDeferredSendTime)
	mustNoErr(t, err, "read the Drafts copy's properties")
	_, stillScheduled := props.Get(mapi.PrDeferredSendTime)
	wantEq(t, stillScheduled, false, "the Drafts copy is still marked as a scheduled send")
}

// TestProcessDueOutboxAttemptBudgetIsPerMessage proves one message's failures do
// not spend another's budget: a message that fails once and then succeeds is
// released normally even after a sibling has been failing all along.
func TestProcessDueOutboxAttemptBudgetIsPerMessage(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	// Fail everything for one sweep short of the budget, then let one through.
	fail := true
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		if fail {
			return nil, errors.New("transport unavailable")
		}
		return nil, nil
	}
	for range maxReleaseAttempts - 1 {
		_, _ = releaseOutbox(context.Background(), st, deliver, nil, time.Now())
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 2 {
		t.Fatalf("Outbox holds %d, want both messages still waiting", n)
	}
	fail = false
	released, err := releaseOutbox(context.Background(), st, deliver, nil, time.Now())
	if err != nil {
		t.Fatalf("release after a recovery: %v", err)
	}
	if released != 2 {
		t.Errorf("released %d, want both messages once delivery recovered", released)
	}
}
