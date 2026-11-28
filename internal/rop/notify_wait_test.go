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
// quiet mailbox and true once a subscribed folder gains a message, the boolean a
// NotificationWait long-poll turns into FLAG_NOTIFICATION_PENDING.
func TestPollForChangeDetectsCreate(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	st := sess.get(logonH).store
	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	dispatchOK(t, sess, buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 0, inboxEID, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)

	if sess.PollForChange() {
		t.Fatal("PollForChange = true on a quiet mailbox, want false")
	}
	info, err := st.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil || info.ID == 0 {
		t.Fatalf("append: id=%d err=%v", info.ID, err)
	}
	if !sess.PollForChange() {
		t.Fatal("PollForChange = false after a message arrived, want true")
	}
}

// TestPollForChangeThenExecuteDrains pins the two-connection seam the live client
// depends on: a NotificationWait connection's PollForChange enqueues the event AND
// advances the snapshot, then a SEPARATE Execute connection drains it. Each half has
// its own unit test, but only this one proves the pending queue carries the detection
// across the two connections, if PollForChange ever detected without enqueuing, both
// halves would stay green while the live flow silently dropped every notification (the
// Execute's own poll finds nothing, since the snapshot already moved).
func TestPollForChangeThenExecuteDrains(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	st := sess.get(logonH).store
	inbox := int64(mapi.PrivateFIDInbox)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	h = dispatchOK(t, sess, buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 0, inboxEID, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)
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
	// PollForChange enqueued. The Execute's own poll finds nothing, the snapshot has
	// already advanced, so delivery rides entirely on the pending queue.
	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("Execute after PollForChange drained no RopNotify (RopId %#x), the wait→execute seam dropped the event", id)
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

// TestSessionConcurrentDispatchPollClose hammers one session from four goroutines,
// a separate-handle writer (the cross-process MTA), a NotificationWait poll loop
// (PollForChange), an Execute loop (Dispatch), and a Disconnect (Close), so
// `go test -race` proves the session mutex actually guards the object table, the
// per-subscription snapshots, and the pending queue against the parallel
// NotificationWait connection. It asserts only the absence of a race or panic; the
// wake signal's correctness is pinned by TestPollForChangeDetectsCreate.
func TestSessionConcurrentDispatchPollClose(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")

	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	inbox := int64(mapi.PrivateFIDInbox)
	// A whole-store subscription so the concurrent poll exercises the all-folders
	// sweep (which mutates the per-folder snapshot map), not just one folder.
	dispatchOK(t, sess, buildRegisterNotification(0, 1, uint8(fnevObjectCreated|fnevObjectModified|fnevObjectDeleted), 1, 0, 0), ropRegisterNotification, "RegisterNotification(whole store)", logonH, 0xFFFFFFFF)

	// The writer opens its own store handle on the same mailbox, mirroring a
	// separate delivering daemon, the session's handle is never touched outside its
	// own locked methods.
	writer, err := objectstore.Open(sess.mailbox)
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	defer mustClose(t, writer)

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
		if info, err := writer.AppendMessage(inbox, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil || info.ID == 0 {
			t.Errorf("deliver into the polled mailbox: id=%d err=%v", info.ID, err)
		}
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

// TestNotifyWaitIgnoresUnsubscribedFolderAndBadHandle proves a scoped Inbox
// subscription does not wake on writes to another folder, and registering on a
// null handle fails with ecError without allocating a subscription handle.
func TestNotifyWaitIgnoresUnsubscribedFolderAndBadHandle(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]

	badResp, badH := sess.Dispatch(buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 0, 0, 0), []uint32{0xFFFFFFFF, 0xFFFFFFFF})
	if ec := readEC(t, badResp, ropRegisterNotification); ec != ecError {
		t.Errorf("RegisterNotification(null handle) ec = %#x, want ecError (%#x)", ec, ecError)
	}
	if badH[1] != 0xFFFFFFFF {
		t.Errorf("RegisterNotification(null handle) allocated %#x, want 0xFFFFFFFF", badH[1])
	}

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	dispatchOK(t, sess, buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 0, inboxEID, 0), ropRegisterNotification, "RegisterNotification(Inbox)", logonH, 0xFFFFFFFF)
	st := sess.get(logonH).store
	sentInfo, err := st.AppendMessage(int64(mapi.PrivateFIDSentItems), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil || sentInfo.ID == 0 {
		t.Fatalf("append Sent Items: id=%d err=%v", sentInfo.ID, err)
	}
	if sess.PollForChange() {
		t.Error("PollForChange = true on unsubscribed Sent Items write, want false")
	}
	if resp := dispatchOn(sess, nil); len(resp) != 0 {
		t.Errorf("Execute drained unexpected notification %x", resp)
	}
}
