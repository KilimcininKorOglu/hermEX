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
		body, _ := io.ReadAll(r.Body)
		got <- string(body)
		_, _ = io.WriteString(w, notificationResult("OK"))
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
