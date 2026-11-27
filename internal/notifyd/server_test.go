package notifyd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testSecret is the shared bearer every test presents: the relay serves nobody
// without one, so there is no unauthenticated shortcut to take here either.
const testSecret = "notifyd-test-secret"

// openStream starts a GET /events consumer and returns the response once its
// headers arrive, at which point the relay has already registered the consumer's
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
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("stream Cache-Control = %q, want no-store", cc)
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

// newRequest builds a request and fails the test on a malformed one.
func newRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, url, err)
	}
	return req
}

// do sends req and returns its status with the body read and closed, so a test
// that only checks the status leaves no connection open behind it.
func do(t *testing.T, ts *httptest.Server, req *http.Request) int {
	t.Helper()
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("read %s body: %v", req.URL.Path, err)
	}
	closeBody(t, resp)
	return resp.StatusCode
}

// closeBody closes a response body and fails the test when the close fails.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

// publish posts ev and returns the relay's status.
func publish(t *testing.T, ts *httptest.Server, bearer string, ev Event) int {
	t.Helper()
	status, err := postEvent(ts, bearer, ev)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return status
}

// postEvent posts ev and returns the status with the body read and closed. It
// takes no *testing.T, so a goroutine other than the test's own can call it.
func postEvent(ts *httptest.Server, bearer string, ev Event) (status int, err error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/publish", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}

// subscribeStatus opens /events with the given Authorization header (none when
// empty) and returns the status, for the requests the relay must refuse.
func subscribeStatus(t *testing.T, ts *httptest.Server, authorization string) int {
	t.Helper()
	req := newRequest(t, http.MethodGet, ts.URL+"/events", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return do(t, ts, req)
}

// TestRefusedPublishReachesNoConsumer: a publish the relay refuses (no bearer, a
// forged bearer, a malformed or an oversized body) must not reach a connected
// consumer. A 401 alone does not prove that, because a relay that broadcast first
// and checked afterwards would still answer 401 while it fed forged wakes to
// every consumer. The first event the consumer reads must be the valid one sent
// after all the refused ones.
func TestRefusedPublishReachesNoConsumer(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()
	resp := openStream(t, ts, testSecret)
	defer closeBody(t, resp)

	forged := Event{Mailbox: "/m/forged", Op: "create", CN: 1}
	if got := publish(t, ts, "", forged); got != http.StatusUnauthorized {
		t.Errorf("publish without bearer = %d, want 401", got)
	}
	if got := publish(t, ts, "wrong", forged); got != http.StatusUnauthorized {
		t.Errorf("publish with a forged bearer = %d, want 401", got)
	}
	bad := newRequest(t, http.MethodPost, ts.URL+"/publish", strings.NewReader(`{"mailbox":"/m/forged"`))
	bad.Header.Set("Authorization", "Bearer "+testSecret)
	if got := do(t, ts, bad); got != http.StatusBadRequest {
		t.Errorf("publish of a truncated body = %d, want 400", got)
	}
	if got := publish(t, ts, testSecret, Event{Mailbox: "/m/forged/" + strings.Repeat("a", 1<<20)}); got != http.StatusBadRequest {
		t.Errorf("oversized publish = %d, want 400", got)
	}

	want := Event{Mailbox: "/m/alice", Op: "modify", CN: 2}
	if got := publish(t, ts, testSecret, want); got != http.StatusNoContent {
		t.Fatalf("valid publish = %d, want 204", got)
	}
	if got := readEvent(t, resp.Body); got != want {
		t.Errorf("first event delivered = %+v, want the valid %+v; a refused publish reached the consumer", got, want)
	}
}

// TestPublishReachesConsumer proves the core relay contract: an event POSTed to
// /publish is delivered verbatim to a connected /events consumer.
func TestPublishReachesConsumer(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()

	resp := openStream(t, ts, testSecret)
	defer closeBody(t, resp)

	want := Event{Mailbox: "/data/mailboxes/user/acme.test/alice", Op: "create", CN: 42, Mid: "7"}
	if got := publish(t, ts, testSecret, want); got != http.StatusNoContent {
		t.Fatalf("publish status = %d, want 204", got)
	}

	got := readEvent(t, resp.Body)
	if got != want {
		t.Errorf("delivered event = %+v, want %+v", got, want)
	}
}

// TestEventRecordIsOneDispatchedSSEEvent reads the raw stream: an event reaches
// an EventSource consumer only when a blank line ends its record (WHATWG HTML,
// Server-sent events, "dispatch the event" on an empty line), so the record must
// be exactly "data: <json>" followed by two line feeds, the JSON on one line.
func TestEventRecordIsOneDispatchedSSEEvent(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()
	resp := openStream(t, ts, testSecret)
	defer closeBody(t, resp)

	ev := Event{Mailbox: "/m/alice", Op: "create", CN: 7, Mid: "3"}
	if got := publish(t, ts, testSecret, ev); got != http.StatusNoContent {
		t.Fatalf("publish status = %d, want 204", got)
	}
	want := `data: {"mailbox":"/m/alice","op":"create","cn":7,"mid":"3"}` + "\n\n"
	got := make([]byte, len(want))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, got)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read record: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no complete record arrived within 2s")
	}
	if string(got) != want {
		t.Errorf("record = %q, want %q", got, want)
	}
}

// TestPublishFanout proves a single publish reaches every connected consumer
// (broadcast, not point-to-point).
func TestPublishFanout(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()

	a := openStream(t, ts, testSecret)
	defer closeBody(t, a)
	b := openStream(t, ts, testSecret)
	defer closeBody(t, b)

	want := Event{Mailbox: "/data/mailboxes/user/acme.test/bob", Op: "flags", CN: 9}
	if got := publish(t, ts, testSecret, want); got != http.StatusNoContent {
		t.Fatalf("publish status = %d, want 204", got)
	}

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
	if got := publish(t, ts, "", Event{Mailbox: "x"}); got != http.StatusUnauthorized {
		t.Errorf("publish without bearer = %d, want 401", got)
	}
	if got := subscribeStatus(t, ts, ""); got != http.StatusUnauthorized {
		t.Errorf("events without bearer = %d, want 401", got)
	}

	// Correct bearer → the stream connects and the event flows.
	resp := openStream(t, ts, "s3cret")
	defer closeBody(t, resp)
	want := Event{Mailbox: "/m/alice", Op: "create", CN: 1}
	if got := publish(t, ts, "s3cret", want); got != http.StatusNoContent {
		t.Fatalf("authorized publish = %d, want 204", got)
	}
	if got := readEvent(t, resp.Body); got != want {
		t.Errorf("delivered = %+v, want %+v", got, want)
	}
}

// TestForgedBearerIsRefused sends bearers that are not the configured secret: a
// wrong one, a prefix of it, the secret with a byte added, and an empty token.
// Each must be refused on both endpoints, or anyone could publish wakes or watch
// which mailboxes receive mail.
func TestForgedBearerIsRefused(t *testing.T) {
	ts := httptest.NewServer(New("s3cret", nil).Handler())
	defer ts.Close()
	for _, forged := range []string{"wrong", "s3cre", "s3cretX", " s3cret", ""} {
		// publish sends no header for an empty token, so the empty case is
		// sent explicitly to cover "Bearer " with nothing after it.
		req := newRequest(t, http.MethodPost, ts.URL+"/publish", strings.NewReader(`{"mailbox":"x"}`))
		req.Header.Set("Authorization", "Bearer "+forged)
		if got := do(t, ts, req); got != http.StatusUnauthorized {
			t.Errorf("publish with bearer %q = %d, want 401", forged, got)
		}
		if got := subscribeStatus(t, ts, "Bearer "+forged); got != http.StatusUnauthorized {
			t.Errorf("events with bearer %q = %d, want 401", forged, got)
		}
	}
}

// TestPublishBadBody rejects a malformed event without disturbing the relay.
func TestPublishBadBody(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()

	req := newRequest(t, http.MethodPost, ts.URL+"/publish", strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer "+testSecret)
	if got := do(t, ts, req); got != http.StatusBadRequest {
		t.Errorf("malformed publish = %d, want 400", got)
	}
}

// TestPublishRefusesAnOversizedEvent posts a well-formed event whose mailbox path
// is a megabyte long. An event is a path and a few ids, so the relay reads at
// most 8 KiB of a body: anything longer is refused, never decoded into memory
// and copied to every consumer.
func TestPublishRefusesAnOversizedEvent(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()

	huge := Event{Mailbox: "/data/" + strings.Repeat("a", 1<<20), Op: "create"}
	if got := publish(t, ts, testSecret, huge); got != http.StatusBadRequest {
		t.Errorf("oversized publish = %d, want 400", got)
	}
}

// TestSlowConsumerDropsRatherThanBlock proves a full consumer buffer drops events
// instead of blocking the publisher. The consumer is a registered channel nobody
// reads, so its buffer really fills: a consumer behind an HTTP stream is drained
// into the socket by its handler, and the kernel buffer absorbs far more events
// than the channel holds. Broadcasting twice the buffer must return promptly and
// leave exactly one buffer of events queued, the rest dropped.
func TestSlowConsumerDropsRatherThanBlock(t *testing.T) {
	s := New(testSecret, nil)
	ch := s.register()
	// unregister runs only once broadcast has returned: a blocked broadcast holds
	// the lock unregister needs, and the test must report that, not hang on it.

	done := make(chan struct{})
	go func() {
		for i := range consumerBuffer * 2 {
			s.broadcast(Event{Mailbox: "/m/x", Op: "create", CN: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher blocked on a full consumer (no drop-on-full)")
	}
	if got := len(ch); got != consumerBuffer {
		t.Errorf("queued events = %d, want the full buffer of %d with the rest dropped", got, consumerBuffer)
	}
	// The queued events are the oldest ones: a full channel drops what arrives,
	// it never replaces what is already waiting.
	if ev := <-ch; ev.CN != 0 {
		t.Errorf("first queued event CN = %d, want 0", ev.CN)
	}
	s.unregister(ch)
}

func TestMethodNotAllowed(t *testing.T) {
	ts := httptest.NewServer(New(testSecret, nil).Handler())
	defer ts.Close()
	req := newRequest(t, http.MethodGet, ts.URL+"/publish", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	if got := do(t, ts, req); got != http.StatusMethodNotAllowed {
		t.Errorf("GET /publish = %d, want 405", got)
	}
}

// TestNoSecretServesNobody proves the relay fails closed when it is built without
// a secret. Both endpoints share one check, and subscribing alone is a live feed
// of which mailbox is receiving and reading mail, so an unset secret must mean
// "serve nobody" rather than "serve everybody". cmd/notify refuses to start in
// this state; this is the second line of defence.
func TestNoSecretServesNobody(t *testing.T) {
	ts := httptest.NewServer(New("", nil).Handler())
	defer ts.Close()

	if got := publish(t, ts, "", Event{Mailbox: "/m/alice"}); got != http.StatusUnauthorized {
		t.Errorf("publish against a secretless relay = %d, want 401", got)
	}
	if got := subscribeStatus(t, ts, ""); got != http.StatusUnauthorized {
		t.Errorf("subscribe against a secretless relay = %d, want 401", got)
	}
	// "Bearer " with an empty token equals the empty secret byte for byte; it
	// must still be refused, or a secretless relay would serve that header.
	if got := subscribeStatus(t, ts, "Bearer "); got != http.StatusUnauthorized {
		t.Errorf("subscribe with an empty bearer against a secretless relay = %d, want 401", got)
	}
}
