package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildRegisterNotification builds a RopRegisterNotification request: header (RopId,
// LogonId, InputHandleIndex) + body (OutputHandleIndex, NotificationTypes, Reserved,
// WantWholeStore, and, only when not whole-store, FolderId, MessageId).
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
// wire EIDs decoded to the objectstore scope, and, the load-bearing invariant, the
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

	// Response: the bare head only, RopId, OutputHandleIndex, ecSuccess, nothing more.
	p := ropOK(t, resp, ropRegisterNotification, "RegisterNotification")
	wantDrained(t, p, "RopRegisterNotification (it has no response body)")

	// The subscription object is registered at the output slot with the decoded scope.
	subH := h[1]
	obj := subscriptionAt(t, sess, subH)
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
	// empty, the load-bearing baseline-at-registration invariant.
	if _, ok := obj.subSnapshot[info.ID]; !ok {
		t.Errorf("baseline snapshot missing pre-existing message %d: %v", info.ID, obj.subSnapshot)
	}
	events, _ := pollChanges(t, st, inbox, obj.subSnapshot, "first poll after registration")
	wantNoEvents(t, events, "first poll after registration (baseline must suppress pre-existing)")
}

// subscriptionAt resolves an output handle to the subscription object it should
// carry, checking the handle was set and echoes back as the RopNotify
// NotificationHandle.
func subscriptionAt(t *testing.T, sess *Session, subH uint32) *object {
	t.Helper()
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
	return obj
}

// TestRegisterNotificationWholeStore confirms a whole-store subscription is accepted
// and given a handle, Outlook commonly registers one, and rejecting it would break
// the client, but is left without a folder baseline, since the all-folders poll it
// needs is deferred (the internal spec §9).
func TestRegisterNotificationWholeStore(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	st := sess.get(logonH).store

	// Seed a message before subscribing so the whole-store baseline captures it.
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	const ntypes = uint8(fnevNewMail | fnevObjectCreated)
	resp, h := sess.Dispatch(buildRegisterNotification(0, 1, ntypes, 1, 0, 0), []uint32{logonH, 0xFFFFFFFF})

	ropOK(t, resp, ropRegisterNotification, "RegisterNotification(whole-store)")

	obj := subscriptionAt(t, sess, h[1])
	if !obj.sub.wholeStore {
		t.Fatalf("whole-store subscription not marked as such: %+v", obj)
	}
	if obj.sub.folderID != 0 || obj.sub.messageID != 0 {
		t.Errorf("whole-store scope = (folder %d, msg %d), want (0, 0)", obj.sub.folderID, obj.sub.messageID)
	}
	if obj.subSnapshot != nil {
		t.Errorf("whole-store subscription set the single-folder snapshot, want nil (it uses the per-folder map)")
	}
	// The baseline spans every content folder and captured the pre-existing message,
	// so the first poll reports nothing for it.
	if len(obj.subFolders) < 2 {
		t.Errorf("whole-store baseline spans %d folders, want the full content tree (>1)", len(obj.subFolders))
	}
	inbox := int64(mapi.PrivateFIDInbox)
	if _, ok := obj.subFolders[inbox][info.ID]; !ok {
		t.Errorf("whole-store baseline missing the pre-existing Inbox message %d: %v", info.ID, obj.subFolders[inbox])
	}
}
