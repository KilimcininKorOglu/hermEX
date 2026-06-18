package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestNotifyDrainDeliversCreate drives the whole poll→classify→drain path end to end:
// a client subscribes to created events on the Inbox, a message is delivered into the
// shared store (as another daemon's MTA would, with no in-process signal), and the
// next Execute — here a bare wake-up with no ROPs — drains the change as a byte-exact
// RopNotify. A second wake-up yields nothing, proving the event is delivered once.
func TestNotifyDrainDeliversCreate(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store
	inbox := int64(mapi.PrivateFIDInbox)

	// Subscribe to created events on the Inbox (empty at subscribe time, so the
	// baseline is empty and the first poll has a clean slate to diff against).
	const ntypes = uint8(fnevObjectCreated)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 0, inboxEID, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	// A message is delivered into the shared store, out of band.
	raw := []byte("From: a@test\r\nTo: b@test\r\nSubject: x\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(inbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// The next Execute drains the create as a RopNotify (no ROPs in the batch — a
	// pure notification wake-up).
	resp, _ := sess.Dispatch(nil, nil)
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropNotify {
		t.Fatalf("RopId = %#x, want RopNotify %#x", id, ropNotify)
	}
	if got := mustU32(t, p, "NotificationHandle"); got != subH {
		t.Errorf("NotificationHandle = %d, want the subscription handle %d", got, subH)
	}
	mustU8(t, p, "LogonId")
	if nflags := mustU16(t, p, "nflags"); nflags != uint16(fnevObjectCreated|nfByMessage) {
		t.Errorf("nflags = %#x, want %#x (created|byMessage)", nflags, fnevObjectCreated|nfByMessage)
	}
	if fid := mustU64(t, p, "FolderId"); fid != inboxEID {
		t.Errorf("FolderId = %#x, want %#x", fid, inboxEID)
	}
	wantMsg := uint64(mapi.MakeEIDEx(1, uint64(info.ID)))
	if mid := mustU64(t, p, "MessageId"); mid != wantMsg {
		t.Errorf("MessageId = %#x, want %#x", mid, wantMsg)
	}
	if cnt := mustU16(t, p, "proptag count"); cnt != 0 {
		t.Errorf("proptag count = %d, want 0 (the create carries no changed-property set)", cnt)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after the RopNotify: %d", p.Remaining())
	}

	// The event was consumed: a second wake-up Execute delivers nothing.
	resp2, _ := sess.Dispatch(nil, nil)
	if len(resp2) != 0 {
		t.Errorf("second poll re-delivered %d bytes, want 0 (the create was already drained)", len(resp2))
	}
}

// TestNotifyDrainOverflowEmitsPendingAndRequeues pins the backpressure contract (§5):
// when the queued notifications overflow the response buffer, the drain emits exactly
// one RopPending and re-queues the remainder — in FIFO order, none dropped — so the
// next Execute delivers them. It populates the queue directly to exercise the drain at
// its byte boundary without seeding tens of thousands of store rows.
func TestNotifyDrainOverflowEmitsPendingAndRequeues(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	// A subscription handle the drain can resolve (a released handle would be dropped).
	subH := sess.alloc(&object{kind: kindSubscription})

	// Each created RopNotify is a fixed 26 bytes: id(1)+handle(4)+logon(1)+nflags(2)+
	// folder(8)+message(8)+proptagcount(2). So exactly notifyBufferCap/26 fit.
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
	// The remainder is the original tail in order — nothing dropped or reordered at
	// the overflow boundary.
	if sess.pending[0].n.messageID != uint64(fit) {
		t.Errorf("first re-queued messageID = %d, want %d", sess.pending[0].n.messageID, fit)
	}

	// A second Execute drains the remainder cleanly, with no further RopPending.
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
// by a message that lands in a folder other than the Inbox — the sweep covers every
// content folder, not just one. A message is delivered into Sent Items and the next
// Execute drains a RopNotify for it.
func TestWholeStoreDeliversCreateInAnyFolder(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	// Whole-store subscription for created events (WantWholeStore=1, no folder scope).
	_, h = sess.Dispatch(buildRegisterNotification(0, 1, uint8(fnevObjectCreated), 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})
	subH := h[1]

	// The message lands in Sent Items, not the Inbox.
	sent := int64(mapi.PrivateFIDSentItems)
	info, err := st.AppendMessage(sent, []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	resp, _ := sess.Dispatch(nil, nil)
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
