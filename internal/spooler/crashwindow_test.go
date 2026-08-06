package spooler

import (
	"context"
	"errors"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestInterruptedReleaseIsNotDeliveredTwice is the duplicate-delivery regression.
// The Outbox removal that records a successful send happens after the send, so a
// process that dies in between used to find the message still scheduled and send
// it again, reaching the external recipients the relay carries. Here the first
// pass sends and then dies (the delivery function panics after handing the mail
// off), and the second pass must not send again.
func TestInterruptedReleaseIsNotDeliveredTwice(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	sends := 0
	crashAfterSending := func([]string, []byte, time.Time) ([]string, error) {
		sends++
		// The mail is out. Everything after this point in the release, including the
		// Outbox removal, is what the crash skips.
		panic("process died after the send")
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = ProcessDueOutbox(context.Background(), st, crashAfterSending, nil, time.Now())
	}()
	if sends != 1 {
		t.Fatalf("the first pass sent %d times, want 1", sends)
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Fatalf("the Outbox holds %d messages after the crash, want 1", n)
	}

	// The process restarts and sweeps again.
	var reported error
	onGiveUp := func(_ []byte, _ []string, cause error) { reported = cause }
	if _, err := ProcessDueOutbox(context.Background(), st, crashAfterSending, onGiveUp, time.Now()); err == nil {
		t.Error("the interrupted release was reported as a clean pass")
	}
	if sends != 1 {
		t.Errorf("the message was sent %d times in total, want 1", sends)
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("the Outbox still holds %d messages; it would be swept again forever", n)
	}
	if n := count(t, st, int64(mapi.PrivateFIDDraft)); n != 1 {
		t.Errorf("Drafts holds %d messages, want the interrupted send handed back", n)
	}
	if !errors.Is(reported, ErrAmbiguousRelease) {
		t.Errorf("the sender was told %v, want the ambiguous-release report", reported)
	}
}

// TestFailedDeliveryStillRetries is the control the stamp must not break. A
// delivery that answers with an error never left, so it belongs on the retry path
// and must not be mistaken for an interrupted release on the next sweep.
func TestFailedDeliveryStillRetries(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	attempts := 0
	failing := func([]string, []byte, time.Time) ([]string, error) {
		attempts++
		return nil, errors.New("mailbox temporarily unavailable")
	}
	for range 3 {
		if _, err := ProcessDueOutbox(context.Background(), st, failing, nil, time.Now()); err == nil {
			t.Fatal("a failed delivery was reported as a clean pass")
		}
	}
	if attempts != 3 {
		t.Errorf("delivery was attempted %d times, want 3; the stamp short-circuited the retries", attempts)
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 1 {
		t.Errorf("the Outbox holds %d messages, want the message still waiting to retry", n)
	}
	if n := count(t, st, int64(mapi.PrivateFIDDraft)); n != 0 {
		t.Errorf("Drafts holds %d messages; a retryable failure was handed back too early", n)
	}
}

// TestSuccessfulReleaseLeavesNoStamp proves the ordinary path is unchanged: a
// message that releases cleanly leaves the Outbox, so no stamp can outlive it and
// nothing is handed back.
func TestSuccessfulReleaseLeavesNoStamp(t *testing.T) {
	st := openStore(t)
	scheduleOutbox(t, st, time.Now().Add(-time.Minute))

	sends := 0
	ok := func([]string, []byte, time.Time) ([]string, error) {
		sends++
		return nil, nil
	}
	released, err := ProcessDueOutbox(context.Background(), st, ok, nil, time.Now())
	if err != nil || released != 1 {
		t.Fatalf("release: released=%d err=%v, want 1, nil", released, err)
	}
	if n := count(t, st, int64(mapi.PrivateFIDOutbox)); n != 0 {
		t.Errorf("the Outbox holds %d messages after a clean release", n)
	}
	if n := count(t, st, int64(mapi.PrivateFIDSentItems)); n != 1 {
		t.Errorf("Sent holds %d messages, want the released copy", n)
	}
	// A second sweep must find nothing left to do.
	if released, err := ProcessDueOutbox(context.Background(), st, ok, nil, time.Now()); err != nil || released != 0 {
		t.Errorf("second sweep: released=%d err=%v, want 0, nil", released, err)
	}
	if sends != 1 {
		t.Errorf("the message was sent %d times, want 1", sends)
	}
	if n := count(t, st, int64(mapi.PrivateFIDDraft)); n != 0 {
		t.Errorf("Drafts holds %d messages after a clean release", n)
	}
}
