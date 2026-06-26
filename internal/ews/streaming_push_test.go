package ews

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStreamWaker is a controllable wake source handing back a per-mailbox channel
// the test fires to simulate a push event for that mailbox.
type fakeStreamWaker struct {
	mu    sync.Mutex
	chans map[string]chan struct{}
}

func (f *fakeStreamWaker) Register(mailbox string) (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.chans[mailbox]
	if !ok {
		ch = make(chan struct{}, 1)
		f.chans[mailbox] = ch
	}
	return ch, func() {}
}

func (f *fakeStreamWaker) fire(mailbox string) {
	f.mu.Lock()
	ch := f.chans[mailbox]
	f.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// TestStreamingWakesViaPush proves the rewired streaming loop emits a continuation
// on a push wake well before its interval: with a 5s interval, a change delivered
// during the held stream surfaces as a CreatedEvent within a fraction of a second
// once the wake fires, where without the wake arm it would wait the full interval.
// The sub-2s arrival proves the push path, not the interval ticker.
func TestStreamingWakesViaPush(t *testing.T) {
	srv, ts, path := streamServer(t)
	srv.streamInterval = 5 * time.Second // long: only a wake surfaces a change fast
	srv.streamWindow = 10 * time.Second
	waker := &fakeStreamWaker{chans: map[string]chan struct{}{}}
	srv.waker = waker

	sess := &session{user: testUser, mailbox: path}
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	inner := `<GetStreamingEvents xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<SubscriptionIds><t:SubscriptionId>` + id + `</t:SubscriptionId></SubscriptionIds>` +
		`<ConnectionTimeout>1</ConnectionTimeout></GetStreamingEvents>`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/EWS/Exchange.asmx", strings.NewReader(wrapRequest(inner)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.SetBasicAuth(testUser, testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	start := time.Now()
	got := make(chan time.Duration, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				if bytes.Contains(acc, []byte("CreatedEvent")) {
					got <- time.Since(start)
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	time.Sleep(300 * time.Millisecond) // let the stream open and register its wake
	seedInbox(t, path, "woke me")      // a delivery during the held stream
	waker.fire(path)                   // the push wake that should emit a continuation now

	select {
	case d := <-got:
		if d > 2*time.Second {
			t.Errorf("CreatedEvent arrived after %v, want < 2s (the wake should beat the 5s interval)", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CreatedEvent never arrived via the push wake (the streaming loop did not wake)")
	}
}
