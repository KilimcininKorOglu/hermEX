package relay

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestSettleFailureDoesNotRedeliver is the defect this state exists for. The
// message is accepted by the mail exchanger and the write that records it fails,
// which is what a locked database, a full disk or a killed process produce. The
// next pass must not open a second SMTP session for that recipient: the remote
// side already has the message.
func TestSettleFailureDoesNotRedeliver(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	raw := []byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n")
	mustNoErr(t, sp.Enqueue("alice@local", []string{"bob@remote"}, raw, t0), "enqueue")

	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
	}

	// First pass: deliver, then simulate the settle never landing by marking the
	// row delivered and leaving it in place, which is exactly the state a failed
	// Sent leaves behind.
	it := claimOne(t, sp, t0)
	mustNoErr(t, sp.MarkStarted(it.RecipientID, t0), "mark started")
	mustNoErr(t, w.deliver(it), "deliver")
	mustNoErr(t, sp.MarkDelivered(it.RecipientID), "mark delivered")
	wantEq(t, len(sink.recorded()), 1, "messages the sink recorded after the first delivery")

	// Second pass, the one that used to duplicate the message.
	_, err := w.ProcessDue(context.Background(), t0.Add(time.Minute))
	mustNoErr(t, err, "second pass")
	wantEq(t, len(sink.recorded()), 1, "messages the recipient received (a settle failure must not redeliver)")

	// The bookkeeping is finished on that pass, so the queue is empty.
	queued, err := sp.List()
	mustNoErr(t, err, "list the queue")
	wantEq(t, len(queued), 0, "recipients left after the settle was retried")
}

// claimOne claims the single recipient a test seeded, stopping the test when the
// spool offers anything else.
func claimOne(t *testing.T, sp *Spool, now time.Time) Item {
	t.Helper()
	items, err := sp.Claim(now, 10)
	mustNoErr(t, err, "claim")
	if len(items) != 1 {
		t.Fatalf("claimed %d items, want 1", len(items))
	}
	return items[0]
}

// TestInterruptedDeliveryGoesBackToTheSender covers the other half: an attempt
// that was started and never concluded. Whether the mail went out cannot be known
// from here, so neither guess is honest, and the sender is told rather than the
// message being sent a second time.
func TestInterruptedDeliveryGoesBackToTheSender(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	mustNoErr(t, sp.Enqueue("alice@local", []string{"bob@remote"},
		[]byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n"), t0), "enqueue")
	it := claimOne(t, sp, t0)
	// A process that died between the stamp and the settle leaves exactly this.
	mustNoErr(t, sp.MarkStarted(it.RecipientID, t0), "mark started")

	var told []string
	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		OnGiveUp: func(it Item, cause error) error {
			told = append(told, it.Recipient)
			return nil
		},
	}
	_, err := w.ProcessDue(context.Background(), t0.Add(time.Minute))
	mustNoErr(t, err, "process")
	wantEq(t, len(sink.recorded()), 0, "messages at the sink (an interrupted delivery must not be sent again)")
	if len(told) != 1 {
		t.Fatalf("the sender was told %d times, want once: %v", len(told), told)
	}
	wantEq(t, told[0], "bob@remote", "the recipient the sender was told about")

	queued, err := sp.List()
	mustNoErr(t, err, "list the queue")
	wantEq(t, len(queued), 0, "recipients left queued after the sender was told")
}

// TestFailedDeliveryClearsTheStamp keeps ordinary retries ordinary: a delivery
// that answered with a failure has a known outcome, so its row must go back on
// the retry path rather than read as interrupted and be bounced.
func TestFailedDeliveryClearsTheStamp(t *testing.T) {
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"},
		[]byte("From: alice@local\r\n\r\nhi\r\n"), t0); err != nil {
		t.Fatal(err)
	}
	var gaveUp int
	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return nil, errors.New("connection refused") },
		OnGiveUp: func(Item, error) error { gaveUp++; return nil },
	}
	if _, err := w.ProcessDue(context.Background(), t0); err != nil {
		t.Fatalf("process: %v", err)
	}
	if gaveUp != 0 {
		t.Errorf("a transient failure was treated as terminal (%d give-ups)", gaveUp)
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("spool holds %d recipient(s), want the deferred one", len(queued))
	}
	if queued[0].Interrupted {
		t.Error("a failed attempt left the row marked interrupted; it must be retried normally")
	}
	if queued[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", queued[0].Attempts)
	}
}

// TestInterruptedBounceStuckClearsStampAndBacksOff proves an interrupted delivery
// whose bounce cannot be delivered is not reprocessed every tick. The first pass
// gives up (bounce fails), clears the in-flight stamp, and defers the row by the
// bounce backoff. A second pass before the backoff expires must not touch the row
// again: without clearing the stamp, Unsettled would re-select it every tick and
// busy-loop the drainer.
func TestInterruptedBounceStuckClearsStampAndBacksOff(t *testing.T) {
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	mustNoErr(t, sp.Enqueue("alice@local", []string{"bob@remote"},
		[]byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n"), t0), "enqueue")
	it := claimOne(t, sp, t0)
	mustNoErr(t, sp.MarkStarted(it.RecipientID, t0), "mark started")

	var gaveUp int
	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return nil, errors.New("no sink") },
		OnGiveUp: func(Item, error) error { gaveUp++; return errors.New("bounce undeliverable") },
	}

	_, err := w.ProcessDue(context.Background(), t0.Add(time.Minute))
	mustNoErr(t, err, "first pass")
	wantEq(t, gaveUp, 1, "give-ups after the first pass")

	// Second pass well before the 6h backoff expires: the row must be untouched.
	_, err = w.ProcessDue(context.Background(), t0.Add(2*time.Minute))
	mustNoErr(t, err, "second pass")
	wantEq(t, gaveUp, 1, "give-ups after the second pass (the stamp must have been cleared)")

	queued, err := sp.List()
	mustNoErr(t, err, "list the queue")
	if len(queued) != 1 {
		t.Fatalf("spool holds %d recipient(s), want the deferred bounce", len(queued))
	}
	wantFalse(t, queued[0].Interrupted, "the stuck bounce is still marked interrupted")
}
