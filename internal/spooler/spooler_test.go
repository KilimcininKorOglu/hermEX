package spooler

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

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

	released, err := ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released %d, want 1", released)
	}

	// Every recipient, including the blind Bcc, must be delivered to.
	for _, want := range []string{"to@example.com", "cc@example.com", "bcc@example.com"} {
		if !slices.Contains(gotRcpts, want) {
			t.Errorf("delivery recipients %v missing %q", gotRcpts, want)
		}
	}
	// The delivered bytes carry To and Cc but never the blind Bcc address (which
	// appears only in the Bcc header, so its absence proves the header was cut).
	dw := string(gotRaw)
	if !strings.Contains(dw, "to@example.com") || !strings.Contains(dw, "cc@example.com") {
		t.Errorf("delivered copy lost To/Cc:\n%s", dw)
	}
	if strings.Contains(dw, "bcc@example.com") {
		t.Errorf("delivered copy leaked the blind Bcc:\n%s", dw)
	}

	// The Outbox is cleared and the Sent copy keeps the Bcc record.
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("Outbox has %d after release, want 0", n)
	}
	sent, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("Sent has %d, want 1", len(sent))
	}
	sentRaw, err := st.GetMessageRaw(int64(mapi.PrivateFIDSentItems), sent[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sentRaw), "bcc@example.com") {
		t.Errorf("Sent copy should keep the Bcc record:\n%s", sentRaw)
	}
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
	released, err := ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now())
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
	released, err := ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now())
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
	// Remove Sent so filing the copy fails for good.
	if err := st.DeleteFolder(int64(mapi.PrivateFIDSentItems)); err != nil {
		t.Fatal(err)
	}

	deliveries := 0
	deliver := func(rcpts []string, raw []byte, when time.Time) ([]string, error) {
		deliveries++
		return nil, nil
	}
	released, err := ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now())
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
	if _, err := ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now()); err != nil {
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
		if _, err := ProcessDueOutbox(context.Background(), st, deliver, onGiveUp, time.Now()); err == nil {
			t.Fatalf("attempt %d: a delivery failure should be reported", i)
		}
		if gaveUp != 0 {
			t.Fatalf("gave up after %d attempts, want only after %d", i, maxReleaseAttempts)
		}
		if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
			t.Fatalf("attempt %d: message left the Outbox early (count=%d)", i, n)
		}
	}

	if _, err := ProcessDueOutbox(context.Background(), st, deliver, onGiveUp, time.Now()); err == nil {
		t.Fatal("the final attempt should report the abandonment")
	}
	if gaveUp != 1 {
		t.Errorf("give-up hook called %d times, want 1", gaveUp)
	}
	if !slices.Contains(gotRecipients, "to@example.com") || !slices.Contains(gotRecipients, "bcc@example.com") {
		t.Errorf("give-up recipients = %v, want every unreached address", gotRecipients)
	}
	if !strings.Contains(string(gotRaw), "Subject: scheduled") {
		t.Error("give-up hook did not receive the message it abandoned")
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("Outbox still holds %d message(s) after giving up, want 0", n)
	}
	drafts, err := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("Drafts holds %d message(s), want the abandoned one", len(drafts))
	}
	props, err := st.GetMessageProperties(drafts[0].ID, mapi.PrDeferredSendTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := props.Get(mapi.PrDeferredSendTime); ok {
		t.Error("the Drafts copy is still marked as a scheduled send")
	}
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
		ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now())
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 2 {
		t.Fatalf("Outbox holds %d, want both messages still waiting", n)
	}
	fail = false
	released, err := ProcessDueOutbox(context.Background(), st, deliver, nil, time.Now())
	if err != nil {
		t.Fatalf("release after a recovery: %v", err)
	}
	if released != 2 {
		t.Errorf("released %d, want both messages once delivery recovered", released)
	}
}
