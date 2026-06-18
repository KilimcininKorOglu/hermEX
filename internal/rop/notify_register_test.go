package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildRegisterNotification builds a RopRegisterNotification request: header (RopId,
// LogonId, InputHandleIndex) + body (OutputHandleIndex, NotificationTypes, Reserved,
// WantWholeStore, and — only when not whole-store — FolderId, MessageId).
func buildRegisterNotification(inIdx, outIdx, ntypes, wantWhole uint8, folderEID, messageEID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropRegisterNotification)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(ntypes)
	b.Uint8(0) // Reserved
	b.Uint8(wantWhole)
	if wantWhole == 0 {
		b.Uint64(folderEID)
		b.Uint64(messageEID)
	}
	return b.Bytes()
}

// TestRegisterNotificationFolderScope drives RopRegisterNotification over a real
// session and pins the three contract points: the bare 6-byte response head (no
// body, HandleIndex = OutputHandleIndex), the subscription object created with the
// wire EIDs decoded to the objectstore scope, and — the load-bearing invariant — the
// baseline snapshot taken at registration so the first poll suppresses a message that
// already existed when the client subscribed.
func TestRegisterNotificationFolderScope(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	// Seed one message into the Inbox before subscribing: the baseline must capture
	// it so the first poll does NOT re-report it as a spurious create.
	inbox := int64(mapi.PrivateFIDInbox)
	raw := []byte("From: a@test\r\nTo: b@test\r\nSubject: x\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(inbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	const ntypes = uint8(fnevObjectCreated | fnevObjectModified | fnevObjectDeleted)
	inboxEID := uint64(mapi.MakeEIDEx(1, uint64(inbox)))
	resp, h := sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 0, inboxEID, 0), []uint32{logonH, 0xFFFFFFFF})

	// Response: the bare head only — RopId, OutputHandleIndex, ecSuccess, nothing more.
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropRegisterNotification {
		t.Fatalf("RopId = %#x, want %#x", id, ropRegisterNotification)
	}
	if oh := mustU8(t, p, "ohindex"); oh != 1 {
		t.Errorf("OutputHandleIndex = %d, want 1", oh)
	}
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ReturnValue = %#x, want 0", ec)
	}
	if p.Remaining() != 0 {
		t.Errorf("RopRegisterNotification has no response body; %d trailing bytes", p.Remaining())
	}

	// The subscription object is registered at the output slot with the decoded scope.
	subH := h[1]
	if subH == 0xFFFFFFFF {
		t.Fatal("subscription handle not set")
	}
	obj := sess.get(subH)
	if obj == nil || obj.kind != kindSubscription {
		t.Fatalf("output handle is not a subscription object: %+v", obj)
	}
	if obj.sub.handle != subH {
		t.Errorf("sub.handle = %d, want %d (echoed as the RopNotify NotificationHandle)", obj.sub.handle, subH)
	}
	if obj.sub.wholeStore {
		t.Error("folder subscription wrongly marked whole-store")
	}
	if obj.sub.types != ntypes {
		t.Errorf("sub.types = %#x, want %#x", obj.sub.types, ntypes)
	}
	if obj.sub.folderID != inbox || obj.sub.messageID != 0 {
		t.Errorf("sub scope = (folder %d, msg %d), want (folder %d, msg 0)", obj.sub.folderID, obj.sub.messageID, inbox)
	}

	// The baseline captured the pre-existing message, so a poll diff against it is
	// empty — the load-bearing baseline-at-registration invariant.
	if _, ok := obj.subSnapshot[info.ID]; !ok {
		t.Errorf("baseline snapshot missing pre-existing message %d: %v", info.ID, obj.subSnapshot)
	}
	events, _, err := detectContentChanges(st, inbox, obj.subSnapshot)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("first poll after registration: got %d events, want 0 (baseline must suppress pre-existing)", len(events))
	}
}

// TestRegisterNotificationWholeStore confirms a whole-store subscription is accepted
// and given a handle — Outlook commonly registers one, and rejecting it would break
// the client — but is left without a folder baseline, since the all-folders poll it
// needs is deferred (the internal spec §9).
func TestRegisterNotificationWholeStore(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	const ntypes = uint8(fnevNewMail | fnevObjectCreated)
	resp, h := sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})

	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropRegisterNotification {
		t.Fatalf("RopId = %#x, want %#x", id, ropRegisterNotification)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("whole-store ReturnValue = %#x, want 0", ec)
	}

	obj := sess.get(h[1])
	if obj == nil || obj.kind != kindSubscription || !obj.sub.wholeStore {
		t.Fatalf("whole-store subscription not registered: %+v", obj)
	}
	if obj.sub.folderID != 0 || obj.sub.messageID != 0 {
		t.Errorf("whole-store scope = (folder %d, msg %d), want (0, 0)", obj.sub.folderID, obj.sub.messageID)
	}
	if obj.subSnapshot != nil {
		t.Errorf("whole-store subscription has a folder snapshot %v, want nil (deferred poll)", obj.subSnapshot)
	}
}
