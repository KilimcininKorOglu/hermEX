package notifyd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// openStream starts a GET /events consumer and returns the response once its
// headers arrive — at which point the relay has already registered the consumer's
// channel, so a subsequent publish cannot race ahead of registration.
func openStream(t *testing.T, ts *httptest.Server, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	return resp
}

// readEvent reads one SSE `data:` record off the stream and decodes it, failing if
// none arrives within the deadline (which would mean the wake never reached the
// consumer).
func readEvent(t *testing.T, body io.Reader) Event {
	t.Helper()
	type res struct {
		ev  Event
		err error
	}
	out := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(body)
		for sc.Scan() {
			line := sc.Text()
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var ev Event
				out <- res{ev, json.Unmarshal([]byte(data), &ev)}
				return
			}
		}
		if err := sc.Err(); err != nil {
			out <- res{err: err}
			return
		}
		out <- res{err: io.EOF}
	}()
	select {
	case r := <-out:
		if r.err != nil {
			t.Fatalf("read event: %v", r.err)
		}
		return r.ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived within 2s")
		return Event{}
	}
}

func publish(t *testing.T, ts *httptest.Server, bearer string, ev Event) *http.Response {
	t.Helper()
	body, _ := json.Marshal(ev)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish", bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return resp
}

// TestPublishReachesConsumer proves the core relay contract: an event POSTed to
// /publish is delivered verbatim to a connected /events consumer.
func TestPublishReachesConsumer(t *testing.T) {
	ts := httptest.NewServer(New("", nil).Handler())
	defer ts.Close()

	resp := openStream(t, ts, "")
	defer resp.Body.Close()

	want := Event{Mailbox: "/data/mailboxes/user/acme.test/alice", Op: "create", CN: 42, Mid: "7"}
	if pr := publish(t, ts, "", want); pr.StatusCode != http.StatusNoContent {
		t.Fatalf("publish status = %d, want 204", pr.StatusCode)
	}

	got := readEvent(t, resp.Body)
	if got != want {
		t.Errorf("delivered event = %+v, want %+v", got, want)
	}
}

// TestPublishFanout proves a single publish reaches every connected consumer
// (broadcast, not point-to-point).
func TestPublishFanout(t *testing.T) {
	ts := httptest.NewServer(New("", nil).Handler())
	defer ts.Close()

	a := openStream(t, ts, "")
	defer a.Body.Close()
	b := openStream(t, ts, "")
	defer b.Body.Close()

	want := Event{Mailbox: "/data/mailboxes/user/acme.test/bob", Op: "flags", CN: 9}
	publish(t, ts, "", want)

	if got := readEvent(t, a.Body); got != want {
		t.Errorf("consumer A = %+v, want %+v", got, want)
	}
	if got := readEvent(t, b.Body); got != want {
		t.Errorf("consumer B = %+v, want %+v", got, want)
	}
}

// TestBearerRequired proves both endpoints reject a request without the shared
// secret when one is configured.
func TestBearerRequired(t *testing.T) {
	ts := httptest.NewServer(New("s3cret", nil).Handler())
	defer ts.Close()

	// No bearer → 401 on both endpoints.
	pub := publish(t, ts, "", Event{Mailbox: "x"})
	pub.Body.Close()
	if pub.StatusCode != http.StatusUnauthorized {
		t.Errorf("publish without bearer = %d, want 401", pub.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/events", nil)
	ev, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	ev.Body.Close()
	if ev.StatusCode != http.StatusUnauthorized {
		t.Errorf("events without bearer = %d, want 401", ev.StatusCode)
	}

	// Correct bearer → the stream connects and the event flows.
	resp := openStream(t, ts, "s3cret")
	defer resp.Body.Close()
	want := Event{Mailbox: "/m/alice", Op: "create", CN: 1}
	if pr := publish(t, ts, "s3cret", want); pr.StatusCode != http.StatusNoContent {
		t.Fatalf("authorized publish = %d, want 204", pr.StatusCode)
	}
	if got := readEvent(t, resp.Body); got != want {
		t.Errorf("delivered = %+v, want %+v", got, want)
	}
}

// TestPublishBadBody rejects a malformed event without disturbing the relay.
func TestPublishBadBody(t *testing.T) {
	ts := httptest.NewServer(New("", nil).Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/publish", strings.NewReader("{not json"))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed publish = %d, want 400", resp.StatusCode)
	}
}

// TestSlowConsumerDropsRatherThanBlock proves a full consumer buffer drops events
// instead of blocking the publisher: publishing far more than the buffer holds, to
// a consumer that never reads, still returns promptly every time.
func TestSlowConsumerDropsRatherThanBlock(t *testing.T) {
	ts := httptest.NewServer(New("", nil).Handler())
	defer ts.Close()

	resp := openStream(t, ts, "") // a consumer that never reads its body
	defer resp.Body.Close()

	done := make(chan struct{})
	go func() {
		for i := range consumerBuffer * 4 {
			pr := publish(t, ts, "", Event{Mailbox: "/m/x", Op: "create", CN: uint64(i)})
			pr.Body.Close()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher blocked on a slow consumer (no drop-on-full)")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	ts := httptest.NewServer(New("", nil).Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/publish", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /publish = %d, want 405", resp.StatusCode)
	}
}
