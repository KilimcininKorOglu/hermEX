package rop

import (
	"sync"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestPollForChangeDetectsCreate pins the wake signal: PollForChange is false on a
// quiet mailbox and true once a subscribed folder gains a message — the boolean a
// NotificationWait long-poll turns into FLAG_NOTIFICATION_PENDING.
func TestPollForChangeDetectsCreate(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store
	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	sess.Dispatch(buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 0, inboxEID, 0), []uint32{logonH, 0xFFFFFFFF})

	if sess.PollForChange() {
		t.Fatal("PollForChange = true on a quiet mailbox, want false")
	}
	if _, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}
	if !sess.PollForChange() {
		t.Fatal("PollForChange = false after a message arrived, want true")
	}
}

// TestSessionConcurrentDispatchPollClose hammers one session from four goroutines —
// a separate-handle writer (the cross-process MTA), a NotificationWait poll loop
// (PollForChange), an Execute loop (Dispatch), and a Disconnect (Close) — so
// `go test -race` proves the session mutex actually guards the object table, the
// per-subscription snapshots, and the pending queue against the parallel
// NotificationWait connection. It asserts only the absence of a race or panic; the
// wake signal's correctness is pinned by TestPollForChangeDetectsCreate.
func TestSessionConcurrentDispatchPollClose(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	sess.Dispatch(buildRegisterNotification(0, 1, uint8(fnevObjectCreated|fnevObjectModified|fnevObjectDeleted), 0, inboxEID, 0), []uint32{logonH, 0xFFFFFFFF})

	// The writer opens its own store handle on the same mailbox, mirroring a
	// separate delivering daemon — the session's handle is never touched outside its
	// own locked methods.
	writer, err := objectstore.Open(sess.mailbox)
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	defer writer.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	run := func(f func()) {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					f()
				}
			}
		})
	}

	run(func() {
		_, _ = writer.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	})
	run(func() { sess.PollForChange() })
	run(func() { sess.Dispatch(nil, nil) })

	// Disconnect fires once, concurrently with the loops above.
	wg.Go(func() {
		time.Sleep(20 * time.Millisecond)
		sess.Close()
	})

	time.Sleep(60 * time.Millisecond)
	close(stop)
	wg.Wait()
}
