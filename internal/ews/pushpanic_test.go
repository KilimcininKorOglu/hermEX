package ews

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/objectstore"
)

// TestAPanickingPushPollKeepsTheWorker makes the first poll of a push
// subscription panic. Each subscription's worker is a goroutine with nothing
// above it, so an unrecovered panic would end the EWS daemon for every user. The
// panic must count as one failed delivery instead: the subscription is left
// unlocked, its worker keeps running, and the next wake delivers the event.
func TestAPanickingPushPollKeepsTheWorker(t *testing.T) {
	srv, sess, path := subServer(t)
	srv.pushAllowInternal = true
	waker := &fakeStreamWaker{chans: map[string]chan struct{}{}}
	srv.waker = waker
	var polls atomic.Int32
	srv.pushPoll = func(st *objectstore.Store, sub *ewsSubscription) ([]notifEvent, error) {
		if polls.Add(1) == 1 {
			panic("event reader bug")
		}
		return pollSubscription(st, sub)
	}

	got := make(chan string, 4)
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			// A handler goroutine cannot fail the test; hand the reader a body no
			// assertion accepts.
			body = []byte("callback body read failed: " + err.Error())
		}
		got <- string(body)
		if _, werr := io.WriteString(w, notificationResult("OK")); werr != nil {
			http.Error(w, werr.Error(), http.StatusInternalServerError)
		}
	}))
	defer cb.Close()

	id := subscribe(t, srv, sess, pushSubscribeInner(cb.URL+"/cb"))
	seedInbox(t, path, "push me")
	waker.fire(path) // this poll panics
	waitFor(t, func() bool { return polls.Load() >= 1 }, "the first poll")

	srv.subMu.Lock()
	sub := srv.subs[id]
	srv.subMu.Unlock()
	waitFor(t, func() bool {
		if !sub.mu.TryLock() {
			return false
		}
		sub.mu.Unlock()
		return true
	}, "the subscription lock released after the panic")

	waker.fire(path)
	select {
	case body := <-got:
		if !strings.Contains(body, "CreatedEvent") {
			t.Errorf("callback did not carry the CreatedEvent: %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the push worker did not survive the panic to deliver the next wake")
	}
	if !srv.removeSubscription(id, sess.user) {
		t.Error("subscription missing after panic recovery")
	}
	srv.subMu.Lock()
	remaining := len(srv.subs)
	srv.subMu.Unlock()
	wantEq(t, "subscriptions after unsubscribe", remaining, 0)
}

// waitFor polls cond for up to three seconds, failing the test with what when it
// never holds.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
