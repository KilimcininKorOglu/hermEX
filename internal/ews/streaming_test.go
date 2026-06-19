package ews

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// streamServer builds an EWS server with a fast streaming cadence and short
// lifetime so a streaming test drives the loop to completion in milliseconds.
func streamServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	path := t.TempDir()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: path}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.streamInterval = 20 * time.Millisecond
	srv.streamWindow = 120 * time.Millisecond
	if st, err := objectstore.Open(path); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, path
}

// streamPost issues a GetStreamingEvents request and returns the full streamed
// response (the client reads to completion, which the short window guarantees).
func streamPost(t *testing.T, ts *httptest.Server, ids []string, timeoutMin int) string {
	t.Helper()
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(`<t:SubscriptionId>` + id + `</t:SubscriptionId>`)
	}
	inner := `<GetStreamingEvents xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<SubscriptionIds>` + b.String() + `</SubscriptionIds>` +
		`<ConnectionTimeout>` + strconv.Itoa(timeoutMin) + `</ConnectionTimeout></GetStreamingEvents>`
	_, body := soapPost(t, ts, wrapRequest(inner), true)
	return body
}

// TestGetStreamingEventsDelivers confirms the stream opens with ConnectionStatus
// OK, delivers a change seeded before the call in the first (immediate)
// continuation, and ends with ConnectionStatus Closed.
func TestGetStreamingEventsDelivers(t *testing.T) {
	srv, ts, path := streamServer(t)
	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))
	seedInbox(t, path, "stream me")

	body := streamPost(t, ts, []string{id}, 1)
	if !strings.Contains(body, ">OK</ConnectionStatus>") {
		t.Errorf("stream must open with ConnectionStatus OK: %s", body)
	}
	if !strings.Contains(body, "CreatedEvent") {
		t.Errorf("a change seeded before the stream must arrive in a continuation: %s", body)
	}
	if !strings.Contains(body, ">Closed</ConnectionStatus>") {
		t.Errorf("stream must end with ConnectionStatus Closed: %s", body)
	}
}

// TestGetStreamingEventsHeartbeat confirms an idle stream emits StatusEvent
// heartbeats and still closes when the window expires.
func TestGetStreamingEventsHeartbeat(t *testing.T) {
	srv, ts, path := streamServer(t)
	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	body := streamPost(t, ts, []string{id}, 1)
	if !strings.Contains(body, "StatusEvent") {
		t.Errorf("an idle stream must emit a StatusEvent heartbeat: %s", body)
	}
	if !strings.Contains(body, ">Closed</ConnectionStatus>") {
		t.Errorf("stream must end Closed: %s", body)
	}
}

// TestGetStreamingEventsAllInvalid confirms a stream over only unknown
// subscriptions reports them in ErrorSubscriptionIds and closes immediately
// rather than holding an idle connection open.
func TestGetStreamingEventsAllInvalid(t *testing.T) {
	_, ts, _ := streamServer(t)
	body := streamPost(t, ts, []string{"Zm9vYmFyMDA="}, 1) // well-formed but unknown
	if !strings.Contains(body, "ErrorInvalidSubscription") {
		t.Errorf("an unknown subscription must report ErrorInvalidSubscription: %s", body)
	}
	if !strings.Contains(body, ">Closed</ConnectionStatus>") {
		t.Errorf("a stream with no live subscription must close immediately: %s", body)
	}
}

// TestGetStreamingEventsExpiryOnEntry confirms the entry sweep evicts an expired
// subscription (closing the streaming-sub eviction gap) and reports it invalid.
func TestGetStreamingEventsExpiryOnEntry(t *testing.T) {
	srv, ts, path := streamServer(t)
	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))
	srv.subMu.Lock()
	srv.subs[id].created = time.Now().Add(-2 * time.Hour)
	srv.subMu.Unlock()

	body := streamPost(t, ts, []string{id}, 1)
	if !strings.Contains(body, "ErrorInvalidSubscription") {
		t.Errorf("an expired subscription must be reported invalid on the stream: %s", body)
	}
	srv.subMu.Lock()
	_, present := srv.subs[id]
	srv.subMu.Unlock()
	if present {
		t.Error("the streaming entry sweep must evict the expired subscription")
	}
}
