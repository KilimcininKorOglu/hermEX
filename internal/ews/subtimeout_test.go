package ews

import (
	"testing"
	"time"
)

// operatorTimeout sets the operator's subscription idle timeout for one test and
// restores the built-in default afterwards. The setting is package scope, like the
// request-body cap, so a test that leaves it set changes every later test.
func operatorTimeout(t *testing.T, minutes int64) {
	t.Helper()
	SetSubscriptionTimeout(minutes)
	t.Cleanup(func() { SetSubscriptionTimeout(0) })
}

// streamingInner builds a StreamingSubscriptionRequest over the whole mailbox.
// [MS-OXWSNTIF] gives this request no Timeout element, which is the whole point:
// the server's own value is the only one there is.
func streamingInner() string {
	return `<Subscribe xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<t:StreamingSubscriptionRequest SubscribeToAllFolders="true">` +
		`<t:EventTypes><t:EventType>NewMailEvent</t:EventType></t:EventTypes>` +
		`</t:StreamingSubscriptionRequest></Subscribe>`
}

// pullInner builds a PullSubscriptionRequest carrying the given Timeout in
// minutes. A timeout of 0 omits the element, which is how a client asks for the
// server's default.
func pullInner(timeoutMin int) string {
	timeout := ""
	if timeoutMin > 0 {
		timeout = `<t:Timeout>` + itoa(int64(timeoutMin)) + `</t:Timeout>`
	}
	return `<Subscribe xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<t:PullSubscriptionRequest SubscribeToAllFolders="true">` +
		`<t:EventTypes><t:EventType>NewMailEvent</t:EventType></t:EventTypes>` +
		timeout + `</t:PullSubscriptionRequest></Subscribe>`
}

// registeredTimeout returns the idle timeout the server stored for a subscription.
func registeredTimeout(t *testing.T, srv *Server, id string) time.Duration {
	t.Helper()
	srv.subMu.Lock()
	defer srv.subMu.Unlock()
	sub, ok := srv.subs[id]
	if !ok {
		t.Fatalf("subscription %s is not registered", id)
	}
	return sub.timeout
}

// TestStreamingSubscriptionTakesTheOperatorTimeout is the load-bearing case. A
// streaming request carries no Timeout, so the server's value is the only one, and
// it used to be a literal shared with the pull default. An operator whose clients
// go offline for longer than that could not raise it.
func TestStreamingSubscriptionTakesTheOperatorTimeout(t *testing.T) {
	srv, sess, _ := subServer(t)
	operatorTimeout(t, 720)

	id := subscribe(t, srv, sess, streamingInner())

	if got := registeredTimeout(t, srv, id); got != 720*time.Minute {
		t.Errorf("streaming subscription timeout = %v, want the operator's 720m", got)
	}
}

// TestStreamingFallsBackToTheBuiltInTimeout keeps an unconfigured deployment on the
// value it had before the setting existed, so the migration changes no behaviour.
func TestStreamingFallsBackToTheBuiltInTimeout(t *testing.T) {
	srv, sess, _ := subServer(t)
	operatorTimeout(t, 0)

	id := subscribe(t, srv, sess, streamingInner())

	want := time.Duration(defaultSubscriptionTimeoutMin) * time.Minute
	if got := registeredTimeout(t, srv, id); got != want {
		t.Errorf("streaming subscription timeout = %v, want the built-in %v", got, want)
	}
}

// TestAPullTimeoutStaysTheClients proves the operator's value does not overrule a
// pull client that asked for its own. [MS-OXWSNTIF] 2.2.4.24 gives a pull
// subscription a Timeout element, and the client's answer is the one that counts.
func TestAPullTimeoutStaysTheClients(t *testing.T) {
	srv, sess, _ := subServer(t)
	operatorTimeout(t, 720)

	id := subscribe(t, srv, sess, pullInner(15))

	if got := registeredTimeout(t, srv, id); got != 15*time.Minute {
		t.Errorf("pull subscription timeout = %v, want the client's 15m", got)
	}
}

// TestAPullWithNoTimeoutTakesTheOperatorValue covers the other half: a client that
// names no Timeout is asking for the server's default, which is the operator's.
func TestAPullWithNoTimeoutTakesTheOperatorValue(t *testing.T) {
	srv, sess, _ := subServer(t)
	operatorTimeout(t, 90)

	id := subscribe(t, srv, sess, pullInner(0))

	if got := registeredTimeout(t, srv, id); got != 90*time.Minute {
		t.Errorf("pull subscription timeout = %v, want the operator's 90m", got)
	}
}

// TestTheOperatorTimeoutIsCappedAtTheSpecCeiling keeps a typo from creating a
// subscription the server would hold for longer than [MS-OXWSNTIF] allows. The
// value also rides in the SubscriptionId as a 32-bit field.
func TestTheOperatorTimeoutIsCappedAtTheSpecCeiling(t *testing.T) {
	srv, sess, _ := subServer(t)
	operatorTimeout(t, 100000)

	id := subscribe(t, srv, sess, streamingInner())

	want := time.Duration(maxSubscriptionTimeoutMin) * time.Minute
	if got := registeredTimeout(t, srv, id); got != want {
		t.Errorf("subscription timeout = %v, want the spec ceiling %v", got, want)
	}
}
