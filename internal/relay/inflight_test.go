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
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, raw, t0); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
	}

	// First pass: deliver, then simulate the settle never landing by marking the
	// row delivered and leaving it in place, which is exactly the state a failed
	// Sent leaves behind.
	items, err := sp.Claim(t0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim: %v (%d items)", err, len(items))
	}
	it := items[0]
	if err := sp.MarkStarted(it.RecipientID, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.deliver(it); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if err := sp.MarkDelivered(it.RecipientID); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.recorded()); got != 1 {
		t.Fatalf("sink recorded %d messages after the first delivery, want 1", got)
	}

	// Second pass, the one that used to duplicate the message.
	if _, err := w.ProcessDue(context.Background(), t0.Add(time.Minute)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := len(sink.recorded()); got != 1 {
		t.Errorf("the recipient received the message %d times; a settle failure must not redeliver", got)
	}
	// The bookkeeping is finished on that pass, so the queue is empty.
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("spool still holds %d recipient(s) after the settle was retried", len(queued))
	}
}

// TestInterruptedDeliveryGoesBackToTheSender covers the other half: an attempt
// that was started and never concluded. Whether the mail went out cannot be known
// from here, so neither guess is honest, and the sender is told rather than the
// message being sent a second time.
func TestInterruptedDeliveryGoesBackToTheSender(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"},
		[]byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n"), t0); err != nil {
		t.Fatal(err)
	}
	items, err := sp.Claim(t0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim: %v (%d items)", err, len(items))
	}
	// A process that died between the stamp and the settle leaves exactly this.
	if err := sp.MarkStarted(items[0].RecipientID, t0); err != nil {
		t.Fatal(err)
	}

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
	if _, err := w.ProcessDue(context.Background(), t0.Add(time.Minute)); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := len(sink.recorded()); got != 0 {
		t.Errorf("an interrupted delivery was sent again (%d message(s) at the sink)", got)
	}
	if len(told) != 1 || told[0] != "bob@remote" {
		t.Errorf("the sender was not told about the interrupted delivery: %v", told)
	}
	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("the interrupted recipient is still queued (%d)", len(queued))
	}
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
	if err := sp.Enqueue("alice@local", []string{"bob@remote"},
		[]byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n"), t0); err != nil {
		t.Fatal(err)
	}
	items, err := sp.Claim(t0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim: %v (%d items)", err, len(items))
	}
	if err := sp.MarkStarted(items[0].RecipientID, t0); err != nil {
		t.Fatal(err)
	}

	var gaveUp int
	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return nil, errors.New("no sink") },
		OnGiveUp: func(Item, error) error { gaveUp++; return errors.New("bounce undeliverable") },
	}

	if _, err := w.ProcessDue(context.Background(), t0.Add(time.Minute)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if gaveUp != 1 {
		t.Fatalf("first pass gave up %d times, want 1", gaveUp)
	}

	// Second pass well before the 6h backoff expires: the row must be untouched.
	if _, err := w.ProcessDue(context.Background(), t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if gaveUp != 1 {
		t.Errorf("the stuck bounce was reprocessed before its backoff (%d give-ups); the stamp was not cleared", gaveUp)
	}

	queued, err := sp.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("spool holds %d recipient(s), want the deferred bounce", len(queued))
	}
	if queued[0].Interrupted {
		t.Error("the stuck bounce is still marked interrupted; the in-flight stamp was not cleared")
	}
}
