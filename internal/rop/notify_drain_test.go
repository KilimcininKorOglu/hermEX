package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestNotifyDrainDeliversCreate drives the whole poll→classify→drain path end to end:
// a client subscribes to created events on the Inbox, a message is delivered into the
// shared store, and the next Execute drains the change as a byte-exact RopNotify
// once. A released subscription handle drops queued events cleanly without breaking
// surviving subscriptions.
func TestNotifyDrainDeliversCreate(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store
	inbox := int64(mapi.PrivateFIDInbox)

	// Subscribe to created events on the Inbox.
	const ntypes = uint8(fnevObjectCreated)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	subH := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 0, inboxEID, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)[1]

	// Also register a second subscription and release it before draining: a released
	// handle must emit nothing while the live subscription delivers normally.
	goneH := dispatchOK(t, sess, buildRegisterNotification(0, 1, ntypes, 0, inboxEID, 0), ropRegisterNotification, "RegisterNotification(released)", logonH, 0xFFFFFFFF)[1]
	if resp := dispatchOn(sess, []byte{ropRelease, 0, 0}, goneH); len(resp) != 0 {
		t.Fatalf("Release(sub) response = %x, want empty", resp)
	}
	if sess.get(goneH) != nil {
		t.Fatalf("released subscription handle %d still resolved in session", goneH)
	}

	// A message is delivered into the shared store, out of band.
	raw := []byte("From: a@test\r\nTo: b@test\r\nSubject: x\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(inbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// The next Execute drains the create as one RopNotify for subH alone.
	assertCreatedNotify(t, dispatchOn(sess, nil), subH, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(info.ID))))

	// The event was consumed: a second wake-up Execute delivers nothing.
	if resp2 := dispatchOn(sess, nil); len(resp2) != 0 {
		t.Errorf("second poll re-delivered %d bytes, want 0 (the create was already drained)", len(resp2))
	}
}

func assertCreatedNotify(t *testing.T, resp []byte, wantSubH uint32, wantInboxEID, wantMsgEID uint64) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != wantSubH {
		t.Errorf("NotificationHandle = %d, want the subscription handle %d", got, wantSubH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != uint16(fnevObjectCreated|nfByMessage) {
		t.Errorf("nflags = %#x, want %#x (created|byMessage)", nflags, fnevObjectCreated|nfByMessage)
	}
	if fid := mustU64(t, p, "FolderId"); fid != wantInboxEID {
		t.Errorf("FolderId = %#x, want %#x", fid, wantInboxEID)
	}
	if mid := mustU64(t, p, "MessageId"); mid != wantMsgEID {
		t.Errorf("MessageId = %#x, want %#x", mid, wantMsgEID)
	}
	if cnt := mustU16(t, p, "proptag count"); cnt != 0 {
		t.Errorf("proptag count = %d, want 0 (the create carries no changed-property set)", cnt)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the RopNotify: %d", p.Remaining())
	}
}

// TestNotifyDrainOverflowEmitsPendingAndRequeues pins the backpressure contract:
// when queued notifications overflow the response buffer, the drain emits exactly
// one RopPending and re-queues the remainder in FIFO order.
func TestNotifyDrainOverflowEmitsPendingAndRequeues(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	// A subscription handle the drain can resolve (plus a stale handle dropped cleanly).
	subH := sess.alloc(&object{kind: kindSubscription})
	sess.pending = append(sess.pending, queuedNotify{handle: 0xDEAD, n: notification{flags: fnevObjectCreated}})

	// Each created RopNotify is 26 bytes; notifyBufferCap/26 fit before overflow.
	const notifySize = 26
	const total = 1300
	fit := notifyBufferCap / notifySize
	for i := range total {
		sess.pending = append(sess.pending, queuedNotify{
			handle: subH,
			n:      notification{flags: fnevObjectCreated | nfByMessage, folderID: 0x1111, messageID: uint64(i)},
		})
	}

	out := ext.NewPush(ext.FlagUTF16)
	sess.drainNotifications(out)

	b := out.Bytes()
	lo := max(len(b)-3, 0)
	if len(b) < 3 || b[len(b)-3] != ropPending || b[len(b)-2] != 0 || b[len(b)-1] != 0 {
		t.Fatalf("drain did not end with a zero-index RopPending; tail % x", b[lo:])
	}
	if len(sess.pending) != total-fit {
		t.Fatalf("re-queued %d notifications, want %d (only %d fit the buffer)", len(sess.pending), total-fit, fit)
	}
	if sess.pending[0].n.messageID != uint64(fit) {
		t.Errorf("first re-queued messageID = %d, want %d", sess.pending[0].n.messageID, fit)
	}

	out2 := ext.NewPush(ext.FlagUTF16)
	sess.drainNotifications(out2)
	if len(sess.pending) != 0 {
		t.Errorf("after the second drain, %d notifications still queued, want 0", len(sess.pending))
	}
	if got, want := out2.Len(), (total-fit)*notifySize; got != want {
		t.Errorf("second drain = %d bytes, want %d (%d notifications, no RopPending)", got, want, total-fit)
	}
}

// TestWholeStoreDeliversCreateInAnyFolder proves a whole-store subscription is woken
// by a message that lands in a folder other than the Inbox.
func TestWholeStoreDeliversCreateInAnyFolder(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()

	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	st := sess.get(logonH).store

	// Whole-store subscription for created events (WantWholeStore=1, no folder scope).
	subH := dispatchOK(t, sess, buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 1, 0, 0), ropRegisterNotification, "RegisterNotification", logonH, 0xFFFFFFFF)[1]

	// The message lands in Sent Items, not the Inbox.
	sent := int64(mapi.PrivateFIDSentItems)
	info, err := st.AppendMessage(sent, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	resp := dispatchOn(sess, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != subH {
		t.Errorf("NotificationHandle = %d, want the whole-store subscription handle %d", got, subH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != uint16(fnevObjectCreated|nfByMessage) {
		t.Errorf("nflags = %#x, want %#x (created|byMessage)", nflags, fnevObjectCreated|nfByMessage)
	}
	if fid := mustU64(t, p, "FolderId"); fid != uint64(mapi.MakeEIDEx(1, uint64(sent))) {
		t.Errorf("FolderId = %#x, want Sent Items %#x", fid, uint64(mapi.MakeEIDEx(1, uint64(sent))))
	}
	if mid := mustU64(t, p, "MessageId"); mid != uint64(mapi.MakeEIDEx(1, uint64(info.ID))) {
		t.Errorf("MessageId = %#x, want %#x", mid, uint64(mapi.MakeEIDEx(1, uint64(info.ID))))
	}
}
