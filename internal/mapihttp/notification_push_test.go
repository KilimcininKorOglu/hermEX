package mapihttp

import (
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/notify"
	"hermex/internal/notifyd"
	"hermex/internal/objectstore"
)

// TestNotificationWaitWakesViaPush proves the push path end-to-end and across the
// real wire: a delivery made through a separate store handle (a different daemon's
// MTA) publishes to a real relay, whose event the server's consumer routes to the
// parked NotificationWait — returning EventPending well under the poll cadence. The
// cadence is set to 10s, so a timely return can ONLY come from the push wake, not a
// cadence poll. It also exercises the path-key match: the publish keys on the
// store's Dir() and the wait registers sess.MailboxDir(), which must be byte-
// identical for the wake to route.
func TestNotificationWaitWakesViaPush(t *testing.T) {
	mailbox := t.TempDir()
	ts, srv := notifyTestServer(t, mailbox)
	srv.notifyWait = 30 * time.Second
	srv.notifyCadence = 10 * time.Second // long: only a push wake can return the wait quickly

	// The full wire chain the daemons use: a real relay, a real consumer subscribed
	// to it, and a real publisher installed as the objectstore change hook.
	relaySrv := httptest.NewServer(notifyd.New("", nil).Handler())
	defer relaySrv.Close()
	consumer := notify.NewConsumer(relaySrv.URL, "", nil)
	defer consumer.Close()
	srv.SetNotify(consumer)
	time.Sleep(200 * time.Millisecond) // let the consumer's stream establish before any publish

	pub := notify.NewPublisher(relaySrv.URL, "")
	objectstore.SetChangePublisher(pub.Publish)
	t.Cleanup(func() { objectstore.SetChangePublisher(nil) })

	sid := notifyConnect(t, ts)
	sc := srv.sessions.lookup(sid)
	if sc == nil {
		t.Fatal("session not found after Connect")
	}
	subscribeInbox(t, sc.ropSess)

	// Begin the wait on a quiet mailbox; it registers the mailbox for a push wake.
	flags := make(chan uint32, 1)
	go func() { flags <- notificationWaitFlags(t, ts, sid) }()
	time.Sleep(100 * time.Millisecond) // let the wait begin and register

	// Deliver from a separate store handle, as a separate daemon's MTA would. The
	// objectstore change publisher POSTs to the relay, which wakes the registration.
	st, err := objectstore.Open(mailbox)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	st.Close()
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	select {
	case got := <-flags:
		if got != flagNotificationPending {
			t.Errorf("push wake: EventPending = %d, want %d", got, flagNotificationPending)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NotificationWait did not wake via push within 3s (cadence is 10s, so the push wake failed)")
	}
}
