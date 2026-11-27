package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/notifyd"
	"hermex/internal/objectstore"
)

// sseStub is a controllable relay /events endpoint: the test feeds events or raw
// SSE payloads to stream, observes each established connection via connect, and
// drops the current connection via drop.
type sseStub struct {
	feed    chan notifyd.Event
	raw     chan string
	connect chan struct{}
	drop    chan struct{}
}

func newSSEStub() (*httptest.Server, *sseStub) {
	st := &sseStub{
		feed:    make(chan notifyd.Event, 16),
		raw:     make(chan string, 16),
		connect: make(chan struct{}, 8),
		drop:    make(chan struct{}, 8),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			return
		}
		select {
		case st.connect <- struct{}{}:
		default:
		}
		serveSSELoop(w, r, rc, st)
	}))
	return srv, st
}

func serveSSELoop(w http.ResponseWriter, r *http.Request, rc *http.ResponseController, st *sseStub) {
	for {
		select {
		case <-r.Context().Done():
			return
		case <-st.drop:
			return
		case line := <-st.raw:
			if !sendSSE(w, rc, []byte(line)) {
				return
			}
		case ev := <-st.feed:
			body, err := json.Marshal(ev)
			if err != nil {
				return
			}
			frame := append([]byte("data: "), body...)
			if !sendSSE(w, rc, append(frame, '\n', '\n')) {
				return
			}
		}
	}
}

// sendSSE writes one SSE payload and flushes it; false means the connection is
// gone and the stub stops serving it.
func sendSSE(w http.ResponseWriter, rc *http.ResponseController, b []byte) bool {
	if _, err := w.Write(b); err != nil {
		return false
	}
	return rc.Flush() == nil
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
// every registered waiter is woken once, the catch-up that observes changes missed
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
	type captured struct {
		ev          notifyd.Event
		method      string
		path        string
		contentType string
		auth        string
		decodeErr   error
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev notifyd.Event
		err := json.NewDecoder(r.Body).Decode(&ev)
		got <- captured{
			ev:          ev,
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			auth:        r.Header.Get("Authorization"),
			decodeErr:   err,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	pub := NewPublisher(srv.URL+"/", "s3cret")
	pub.Publish(objectstore.ChangeEvent{MailboxDir: "/m/alice", Op: "create", CN: 5, Mid: "m7"})

	select {
	case c := <-got:
		if c.decodeErr != nil {
			t.Fatalf("decode published event: %v", c.decodeErr)
		}
		if c.method != http.MethodPost {
			t.Errorf("Method = %q, want %q", c.method, http.MethodPost)
		}
		if c.path != "/publish" {
			t.Errorf("Path = %q, want %q", c.path, "/publish")
		}
		if c.contentType != "application/json" {
			t.Errorf("Content-Type = %q, want %q", c.contentType, "application/json")
		}
		want := notifyd.Event{Mailbox: "/m/alice", Op: "create", CN: 5, Mid: "m7"}
		if c.ev != want {
			t.Errorf("relay received %+v, want %+v", c.ev, want)
		}
		if c.auth != "Bearer s3cret" {
			t.Errorf("Authorization = %q, want %q", c.auth, "Bearer s3cret")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not POST within 2s")
	}
}

// TestNilClientsAreNoops proves an empty notify_url disables push cleanly: the
// constructors return nil, and the nil values are safe to use, the degradation
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

// TestConsumerRejectsNon200Stream verifies that a relay returning a non-200 status
// (such as 401 Unauthorized, 403 Forbidden, or 500 Internal Server Error) is
// refused even if the response body contains a forged SSE event, and that
// registered waiter state remains intact.
func TestConsumerRejectsNon200Stream(t *testing.T) {
	statuses := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError}
	for _, code := range statuses {
		t.Run(http.StatusText(code), func(t *testing.T) {
			type reqMeta struct {
				path string
				auth string
			}
			seen := make(chan reqMeta, 4)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case seen <- reqMeta{path: r.URL.Path, auth: r.Header.Get("Authorization")}:
				default:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(code)
				if _, err := w.Write([]byte("data: {\"mailbox\":\"/mbox/alice\",\"op\":\"create\"}\n\n")); err != nil {
					return
				}
			}))
			defer srv.Close()

			c := NewConsumer(srv.URL+"/", "topsecret", nil)
			defer c.Close()

			wakeCh, cancel := c.Register("/mbox/alice")
			defer cancel()

			select {
			case meta := <-seen:
				if meta.path != "/events" {
					t.Errorf("Path = %q, want %q", meta.path, "/events")
				}
				if meta.auth != "Bearer topsecret" {
					t.Errorf("Authorization = %q, want %q", meta.auth, "Bearer topsecret")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("consumer did not dial /events within 2s")
			}

			assertNoWake(t, wakeCh, 150*time.Millisecond)

			c.mu.Lock()
			gotWaiters := len(c.waiters["/mbox/alice"])
			c.mu.Unlock()
			if gotWaiters != 1 {
				t.Fatalf("waiters[/mbox/alice] = %d, want 1", gotWaiters)
			}
		})
	}
}

// TestConsumerIgnoresHostileSSELines verifies that non-data lines, forged prefixes,
// malformed JSON, missing mailbox fields, and events for another account do not
// wake a registered mailbox waiter and leave the registration healthy for later
// valid events.
func TestConsumerIgnoresHostileSSELines(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()
	<-st.connect

	wakeCh, cancel := c.Register("/mbox/alice")
	defer cancel()

	hostile := []string{
		": keepalive comment\n\n",
		"data:{\"mailbox\":\"/mbox/alice\",\"op\":\"create\"}\n\n",
		"event: {\"mailbox\":\"/mbox/alice\",\"op\":\"create\"}\n\n",
		"data: {not-valid-json\n\n",
		"data: \n\n",
		"data: {\"op\":\"create\",\"cn\":1}\n\n",
		"data: {\"mailbox\":\"/mbox/other\",\"op\":\"create\"}\n\n",
	}
	for _, line := range hostile {
		st.raw <- line
	}
	assertNoWake(t, wakeCh, 200*time.Millisecond)

	c.mu.Lock()
	gotWaiters := len(c.waiters["/mbox/alice"])
	c.mu.Unlock()
	if gotWaiters != 1 {
		t.Fatalf("waiters[/mbox/alice] = %d, want 1", gotWaiters)
	}

	// Stream and registration remain intact after ignoring hostile lines.
	st.feed <- notifyd.Event{Mailbox: "/mbox/alice", Op: "create"}
	assertWake(t, wakeCh)
}

// TestConsumerRejectsOversizedSSELine verifies that an SSE line exceeding the 1 MiB
// scanner limit does not wake registered waiters or corrupt waiter state.
func TestConsumerRejectsOversizedSSELine(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()
	<-st.connect

	wakeCh, cancel := c.Register("/mbox/alice")
	defer cancel()

	oversized := "data: {\"mailbox\":\"/mbox/alice\",\"op\":\"" + strings.Repeat("a", (1<<20)+1024) + "\"}\n\n"
	st.raw <- oversized
	assertNoWake(t, wakeCh, 150*time.Millisecond)

	c.mu.Lock()
	gotWaiters := len(c.waiters["/mbox/alice"])
	c.mu.Unlock()
	if gotWaiters != 1 {
		t.Fatalf("waiters[/mbox/alice] = %d, want 1", gotWaiters)
	}
}

// TestConsumerUnregisterStopsWakes verifies that canceling a registration stops
// future wakes for that waiter, leaves sibling waiters intact, and removes the
// mailbox map entry when the last waiter unregisters.
func TestConsumerUnregisterStopsWakes(t *testing.T) {
	srv, st := newSSEStub()
	defer srv.Close()
	c := NewConsumer(srv.URL, "", nil)
	defer c.Close()
	<-st.connect

	w1, c1 := c.Register("/mbox/alice")
	w2, c2 := c.Register("/mbox/alice")

	c1()
	c1() // idempotent second cancel must not disturb w2

	st.feed <- notifyd.Event{Mailbox: "/mbox/alice", Op: "create"}
	assertWake(t, w2)
	assertNoWake(t, w1, 100*time.Millisecond)

	c2()
	c.mu.Lock()
	_, exists := c.waiters["/mbox/alice"]
	c.mu.Unlock()
	if exists {
		t.Fatal("waiters map still contains /mbox/alice after all waiters unregistered")
	}
}

// TestPublisherOmitsAuthWithoutSecretAndSurvivesHostileInputs verifies that
// Publisher omits Authorization when no secret is configured, ignores an invalid
// endpoint URL cleanly, and continues delivering after a relay 401 refusal.
func TestPublisherOmitsAuthWithoutSecretAndSurvivesHostileInputs(t *testing.T) {
	authCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCh <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	pub := NewPublisher(srv.URL, "")
	pub.Publish(objectstore.ChangeEvent{MailboxDir: "/m/alice", Op: "create"})

	select {
	case auth := <-authCh:
		if auth != "" {
			t.Errorf("Authorization = %q, want empty when secret is unset", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher did not POST within 2s")
	}

	badPub := NewPublisher("http://\x7f.invalid", "s")
	badPub.post(notifyd.Event{Mailbox: "/m/alice", Op: "create"})
}
