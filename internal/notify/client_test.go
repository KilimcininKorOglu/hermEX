package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/notifyd"
	"hermex/internal/objectstore"
)

// sseStub is a controllable relay /events endpoint: the test feeds events to
// stream, observes each established connection via connect, and drops the current
// connection via drop — enough to exercise wake, coalescing, and reconnect.
type sseStub struct {
	feed    chan notifyd.Event
	connect chan struct{}
	drop    chan struct{}
}

func newSSEStub() (*httptest.Server, *sseStub) {
	st := &sseStub{
		feed:    make(chan notifyd.Event, 16),
		connect: make(chan struct{}, 8),
		drop:    make(chan struct{}, 8),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		select {
		case st.connect <- struct{}{}:
		default:
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-st.drop:
				return
			case ev := <-st.feed:
				body, _ := json.Marshal(ev)
				w.Write([]byte("data: "))
				w.Write(body)
				w.Write([]byte("\n\n"))
				if rc.Flush() != nil {
					return
				}
			}
		}
	}))
	return srv, st
}

func assertWake(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a wake within 2s, got none")
	}
}

func assertNoWake(t *testing.T, ch <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected wake")
	case <-time.After(within):
	}
}

// TestConsumerWakesOnEvent proves a relayed event wakes the matching mailbox's
// waiter and only that mailbox's.
func TestConsumerWakesOnEvent(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()
	<-st.connect

	wakeCh, cancel := c.Register("/mbox/alice")
	defer cancel()

	st.feed <- notifyd.Event{Mailbox: "/mbox/alice", Op: "create"}
	assertWake(t, wakeCh)

	// An event for another mailbox must not wake this waiter.
	st.feed <- notifyd.Event{Mailbox: "/mbox/bob", Op: "create"}
	assertNoWake(t, wakeCh, 250*time.Millisecond)
}

// TestConsumerCoalesces proves a burst of events collapses to one pending wake (the
// channel is buffered size 1), so a waiter that diffs once per wake is not flooded.
func TestConsumerCoalesces(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()
	<-st.connect

	wakeCh, cancel := c.Register("/m")
	defer cancel()

	for range 3 {
		st.feed <- notifyd.Event{Mailbox: "/m", Op: "create"}
	}
	time.Sleep(150 * time.Millisecond) // let all three be processed
	assertWake(t, wakeCh)
	assertNoWake(t, wakeCh, 150*time.Millisecond) // coalesced: only one pending
}

// TestConsumerMultiWaiter proves one event wakes every long-poll registered on the
// same mailbox (a mailbox may have several open sessions).
func TestConsumerMultiWaiter(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()
	<-st.connect

	w1, c1 := c.Register("/m")
	defer c1()
	w2, c2 := c.Register("/m")
	defer c2()

	st.feed <- notifyd.Event{Mailbox: "/m", Op: "flags"}
	assertWake(t, w1)
	assertWake(t, w2)
}

// TestConsumerReconnectFiresAll proves that after the stream drops and reconnects,
// every registered waiter is woken once — the catch-up that observes changes missed
// during the disconnect gap.
func TestConsumerReconnectFiresAll(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()

	wakeCh, cancel := c.Register("/m")
	defer cancel()

	<-st.connect // connection #1 (no catch-up wake on first connect)
	assertNoWake(t, wakeCh, 100*time.Millisecond)

	st.drop <- struct{}{} // drop the connection
	<-st.connect          // connection #2 (reconnect)
	assertWake(t, wakeCh) // the reconnect catch-up woke the waiter
}

// TestPublisherPostsEvent proves a producer's change event is POSTed to the relay
// in the wire shape the relay decodes (objectstore field names mapped to the wire
// keys), with the bearer when configured.
func TestPublisherPostsEvent(t *testing.T) {
	got := make(chan notifyd.Event, 1)
	auth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev notifyd.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		auth <- r.Header.Get("Authorization")
		got <- ev
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	pub := NewPublisher(srv.URL, "s3cret")
	pub.Publish(objectstore.ChangeEvent{MailboxDir: "/m/alice", Op: "create", CN: 5, Mid: "m7"})

	select {
	case ev := <-got:
		want := notifyd.Event{Mailbox: "/m/alice", Op: "create", CN: 5, Mid: "m7"}
		if ev != want {
			t.Errorf("relay received %+v, want %+v", ev, want)
		}
		if a := <-auth; a != "Bearer s3cret" {
			t.Errorf("Authorization = %q, want %q", a, "Bearer s3cret")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not POST within 2s")
	}
}

// TestNilClientsAreNoops proves an empty notify_url disables push cleanly: the
// constructors return nil, and the nil values are safe to use — the degradation
// floor a daemon relies on when push is not configured.
func TestNilClientsAreNoops(t *testing.T) {
	if NewPublisher("", "s") != nil {
		t.Error("empty url should yield a nil Publisher")
	}
	var p *Publisher
	p.Publish(objectstore.ChangeEvent{Op: "create"}) // must not panic

	if NewConsumer("", "s", nil) != nil {
		t.Error("empty url should yield a nil Consumer")
	}
	var c *Consumer
	ch, cancel := c.Register("/m")
	if ch != nil {
		t.Error("nil Consumer Register should return a nil channel")
	}
	cancel()  // must not panic
	c.Close() // must not panic
}
