package rop

import (
	"sync"
	"testing"
	"time"

	"hermex/internal/ext"
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

// TestPollForChangeThenExecuteDrains pins the two-connection seam the live client
// depends on: a NotificationWait connection's PollForChange enqueues the event AND
// advances the snapshot, then a SEPARATE Execute connection drains it. Each half has
// its own unit test, but only this one proves the pending queue carries the detection
// across the two connections — if PollForChange ever detected without enqueuing, both
// halves would stay green while the live flow silently dropped every notification (the
// Execute's own poll finds nothing, since the snapshot already moved).
func TestPollForChangeThenExecuteDrains(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store
	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 0, inboxEID, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	info, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// The NotificationWait connection detects the change and advances the snapshot.
	if !sess.PollForChange() {
		t.Fatal("PollForChange = false, want true after a delivery")
	}

	// A separate Execute connection (here an empty wake-up batch) drains the RopNotify
	// PollForChange enqueued. The Execute's own poll finds nothing — the snapshot has
	// already advanced — so delivery rides entirely on the pending queue.
	resp, _ := sess.Dispatch(nil, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("Execute after PollForChange drained no RopNotify (RopId %#x) — the wait→execute seam dropped the event", id)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != subH {
		t.Errorf("NotificationHandle = %d, want %d", got, subH)
	}
	mustU8(t, p, "LogonId")
	mustU16(t, p, "nflags")
	if fid := mustU64(t, p, "FolderId"); fid != inboxEID {
		t.Errorf("FolderId = %#x, want %#x", fid, inboxEID)
	}
	if mid := mustU64(t, p, "MessageId"); mid != uint64(mapi.MakeEIDEx(1, uint64(info.ID))) {
		t.Errorf("MessageId = %#x, want %#x", mid, uint64(mapi.MakeEIDEx(1, uint64(info.ID)))) // the enqueued event
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
	// A whole-store subscription so the concurrent poll exercises the all-folders
	// sweep (which mutates the per-folder snapshot map), not just one folder.
	sess.Dispatch(buildRegisterNotification(0, 1, uint8(fnevObjectCreated|fnevObjectModified|fnevObjectDeleted), 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})

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
