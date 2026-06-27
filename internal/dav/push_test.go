package dav

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/notify"
	"hermex/internal/notifyd"
	"hermex/internal/objectstore"
)

// TestPushPollWakesOnChange confirms a DAV push long-poll, registered on the central
// wake bus, returns sub-second when a change is published for its mailbox -- the #118
// verify, gated on LATENCY (the cadence floor is 30s, so a return within 5s can only be
// a wake, not a poll timeout).
func TestPushPollWakesOnChange(t *testing.T) {
	relay := httptest.NewServer(notifyd.New("", nil).Handler())
	t.Cleanup(relay.Close)
	consumer := notify.NewConsumer(relay.URL, "", nil)
	t.Cleanup(consumer.Close)
	publisher := notify.NewPublisher(relay.URL, "")

	mboxDir := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mboxDir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: mboxDir}}
	srv := NewServer(accs, accs, "hermex.test")
	srv.SetNotify(consumer)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Let the consumer's SSE stream connect to the relay before any publish (the bus is
	// fire-and-forget, so a pre-connection event would be missed).
	time.Sleep(400 * time.Millisecond)

	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", ts.URL+"/dav/push", nil)
		req.SetBasicAuth(testUser, testPass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()

	// Let the long-poll register on the bus, then publish a change for its mailbox.
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	publisher.Publish(objectstore.ChangeEvent{MailboxDir: mboxDir, Op: "create"})

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("push poll status %d, want 200 (woke)", code)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("push poll took %v to wake; cadence is %v, so a wake should be sub-second", elapsed, pushPollCadence)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("push long-poll never returned after a published change")
	}
}
