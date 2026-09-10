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
		b.WriteString(`<t:SubscriptionId>`)
		b.WriteString(id)
		b.WriteString(`</t:SubscriptionId>`)
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
	// The XML declaration rides only the first chunk; continuations are decl-less.
	if n := strings.Count(body, "<?xml"); n != 1 {
		t.Errorf("XML declaration must appear once (first chunk only), got %d: %s", n, body)
	}
}

// TestGetStreamingEventsMultiSub confirms several subscriptions stream over one
// connection: each gets its own Notification in a continuation, and a change in
// one subscription's scope does not surface under another. Multi-subscription is
// the primary streaming case (Outlook batches all its folders onto one stream).
func TestGetStreamingEventsMultiSub(t *testing.T) {
	srv, ts, path := streamServer(t)
	sess := &session{user: testUser, mailbox: path}
	inbox := subscribe(t, srv, sess, subscribeInner(false, "inbox", "CreatedEvent"))
	sent := subscribe(t, srv, sess, subscribeInner(false, "sentitems", "CreatedEvent"))
	seedInbox(t, path, "for inbox sub")

	body := streamPost(t, ts, []string{inbox, sent}, 1)
	if !strings.Contains(body, inbox) || !strings.Contains(body, sent) {
		t.Errorf("both subscriptions must appear in the stream (per-sub Notifications): %s", body)
	}
	if !strings.Contains(body, "CreatedEvent") {
		t.Errorf("the inbox change must surface for the inbox subscription: %s", body)
	}
	// The change is in inbox scope only; the sentitems sub never sees a create,
	// the demux guarantee (a whole-store sub instead would also see it).
	if !strings.Contains(body, "StatusEvent") {
		t.Errorf("the out-of-scope subscription must heartbeat, not carry the create: %s", body)
	}
}

// TestGetStreamingEventsMixedValidInvalid confirms one invalid id fails the whole
// call: the response names it in ErrorSubscriptionIds, reports the connection
// Closed, and streams nothing, even though another named subscription is live.
// The documented GetStreamingEvents error response pairs ErrorInvalidSubscription
// with ConnectionStatus Closed, and the operation confirms each subscription id
// before it streams.
func TestGetStreamingEventsMixedValidInvalid(t *testing.T) {
	srv, ts, path := streamServer(t)
	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	body := streamPost(t, ts, []string{id, "Zm9vYmFyMDA="}, 1)
	if !strings.Contains(body, "ErrorInvalidSubscription") {
		t.Errorf("the unknown id must be reported in ErrorSubscriptionIds: %s", body)
	}
	if !strings.Contains(body, ">Closed</ConnectionStatus>") {
		t.Errorf("an invalid subscription must report the connection Closed: %s", body)
	}
	if strings.Contains(body, ">OK</ConnectionStatus>") {
		t.Errorf("a refused call must never report the connection OK: %s", body)
	}
	if strings.Contains(body, "StatusEvent") {
		t.Errorf("a refused call must stream nothing: %s", body)
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
// rather than holding an idle connection open. The error message itself carries
// the Closed status: a client that reads connection state rather than the
// response code otherwise sees no reason to subscribe anew.
func TestGetStreamingEventsAllInvalid(t *testing.T) {
	_, ts, _ := streamServer(t)
	body := streamPost(t, ts, []string{"Zm9vYmFyMDA="}, 1) // well-formed but unknown
	if !strings.Contains(body, "ErrorInvalidSubscription") {
		t.Errorf("an unknown subscription must report ErrorInvalidSubscription: %s", body)
	}
	if !strings.Contains(body, ">Closed</ConnectionStatus>") {
		t.Errorf("a stream with no live subscription must close immediately: %s", body)
	}
	if strings.Contains(body, ">OK</ConnectionStatus>") {
		t.Errorf("the refusal must not report the connection OK anywhere: %s", body)
	}
}

// TestStreamChunkReportsALostSubscription is the mid-stream unit case: a
// subscription that vanished while the connection was held is named in
// ErrorSubscriptionIds on the very chunk that notices it, and drops out of the
// watched set, while the live one keeps its Notification.
func TestStreamChunkReportsALostSubscription(t *testing.T) {
	srv, _, path := streamServer(t)
	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	msg, live := srv.streamChunk([]string{id, "Zm9vYmFyMDA="})

	if msg.ResponseClass != "Error" || msg.ResponseCode != "ErrorInvalidSubscription" {
		t.Errorf("chunk = %s/%s, want Error/ErrorInvalidSubscription", msg.ResponseClass, msg.ResponseCode)
	}
	if msg.ErrorSubs == nil || len(msg.ErrorSubs.IDs) != 1 || msg.ErrorSubs.IDs[0] != "Zm9vYmFyMDA=" {
		t.Errorf("ErrorSubscriptionIds = %+v, want the lost id", msg.ErrorSubs)
	}
	if len(live) != 1 || live[0] != id {
		t.Errorf("live = %v, want only the surviving subscription", live)
	}
	if len(msg.Notifications) != 1 {
		t.Errorf("notifications = %d, want 1 (the surviving subscription)", len(msg.Notifications))
	}
}

// TestStreamClosesWhenItsLastSubscriptionIsLost proves the held connection ends as
// soon as nothing is left to watch: the subscription is live when the stream
// opens and its lifetime passes during the stream, so the loop reports it and
// closes long before the connection window would.
func TestStreamClosesWhenItsLastSubscriptionIsLost(t *testing.T) {
	srv, ts, path := streamServer(t)
	srv.streamWindow = 2 * time.Second
	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))
	srv.subMu.Lock()
	srv.subs[id].created = time.Now()
	srv.subs[id].timeout = 60 * time.Millisecond // live on entry, gone a few ticks later
	srv.subMu.Unlock()

	start := time.Now()
	body := streamPost(t, ts, []string{id}, 1)
	elapsed := time.Since(start)

	if !strings.Contains(body, "ErrorInvalidSubscription") {
		t.Errorf("the lost subscription must be reported on the stream: %s", body)
	}
	if !strings.Contains(body, ">Closed</ConnectionStatus>") {
		t.Errorf("the stream must report the connection Closed: %s", body)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("the stream held for %v, want a close as soon as the last subscription was lost", elapsed)
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
