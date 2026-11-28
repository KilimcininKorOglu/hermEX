package rop

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestDetectContentChanges proves the poll detector turns shared-store mutations
// into the right notifications: a create on append, a modify when a message's
// counter advances (including a read-state flip, which moves read_cn rather than
// change_number), and a delete when an id vanishes. It drives a real objectstore so
// the change-number/read_cn behaviour is exercised end to end, not mocked.
func TestDetectContentChanges(t *testing.T) {
	st, err := objectstore.Open(provisionedDir(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer mustClose(t, st)
	inbox := int64(mapi.PrivateFIDInbox)
	wantFolder := uint64(mapi.MakeEIDEx(1, uint64(inbox)))

	// Empty inbox: no changes, empty snapshot.
	events, snap := pollChanges(t, st, inbox, nil, "baseline")
	wantNoEvents(t, events, "baseline")

	// Append: one ObjectCreated for the new message, with wire EIDs.
	raw := []byte("From: a@test\r\nTo: b@test\r\nSubject: x\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(inbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	wantMsg := uint64(mapi.MakeEIDEx(1, uint64(info.ID)))

	events, snap = pollChanges(t, st, inbox, snap, "create")
	ev := wantOneEvent(t, events, fnevObjectCreated|nfByMessage, "create")
	if ev.folderID != wantFolder || ev.messageID != wantMsg {
		t.Errorf("create eids: folder=%#x msg=%#x, want folder=%#x msg=%#x",
			ev.folderID, ev.messageID, wantFolder, wantMsg)
	}

	// Read-state flip advances read_cn, not change_number, the MAX-based snapshot
	// must still see it as a modify (the discriminating check for the read_cn fix).
	if err := st.SetMessageReadState(info.ID, true); err != nil {
		t.Fatalf("set read state: %v", err)
	}
	events, snap = pollChanges(t, st, inbox, snap, "modify")
	wantOneEvent(t, events, fnevObjectModified|nfByMessage, "read-state modify")

	// A poll with no further change emits nothing.
	events, snap = pollChanges(t, st, inbox, snap, "idle")
	wantNoEvents(t, events, "idle")

	// Delete: one ObjectDeleted carrying the gone message's id.
	if err := st.DeleteMessage(inbox, info.UID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	events, snap = pollChanges(t, st, inbox, snap, "delete")
	ev = wantOneEvent(t, events, fnevObjectDeleted|nfByMessage, "delete")
	if ev.messageID != wantMsg {
		t.Errorf("delete msg eid = %#x, want %#x", ev.messageID, wantMsg)
	}
	if len(snap) != 0 {
		t.Errorf("snapshot after delete has %d entries, want 0", len(snap))
	}
}

// TestDetectContentChangesIsolatesFolders proves changes in another folder or
// polling a nonexistent folder id emit no Inbox events and keep the Inbox
// snapshot and stored messages unchanged.
func TestDetectContentChangesIsolatesFolders(t *testing.T) {
	dir := provisionedDir(t)
	inboxID := seedInboxMessage(t, dir, "Keep")
	st, err := objectstore.Open(dir)
	mustNil(t, err)
	defer mustClose(t, st)
	inbox := int64(mapi.PrivateFIDInbox)
	sent := int64(mapi.PrivateFIDSentItems)

	events, snap := pollChanges(t, st, inbox, nil, "initial")
	wantOneEvent(t, events, fnevObjectCreated|nfByMessage, "initial")

	sentInfo, err := st.AppendMessage(sent, []byte("From: a@test\r\nSubject: s\r\n\r\nhi\r\n"), time.Unix(1700000001, 0), 0)
	if err != nil || sentInfo.ID == 0 {
		t.Fatalf("append Sent Items: id=%d err=%v", sentInfo.ID, err)
	}
	events, snapAfter := pollChanges(t, st, inbox, snap, "cross-folder")
	wantNoEvents(t, events, "cross-folder")
	if len(snapAfter) != 1 || snapAfter[inboxID] != snap[inboxID] {
		t.Errorf("Inbox snapshot mutated by Sent Items write: got %v, want %v", snapAfter, snap)
	}

	bogusEv, bogusSnap := pollChanges(t, st, 0x7FFFFF, nil, "missing-folder")
	wantNoEvents(t, bogusEv, "missing-folder")
	if len(bogusSnap) != 0 {
		t.Errorf("nonexistent folder snapshot = %v, want empty", bogusSnap)
	}
	if f := messageFlags(t, dir, inbox, inboxID); f == -1 {
		t.Error("Inbox message disappeared during isolation poll")
	}
}

// pollChanges runs one detection pass and fails the test when it errors.
func pollChanges(t *testing.T, st *objectstore.Store, folderID int64, snap folderSnapshot, what string) ([]notification, folderSnapshot) {
	t.Helper()
	events, next, err := detectContentChanges(st, folderID, snap)
	if err != nil {
		t.Fatalf("detect %s: %v", what, err)
	}
	return events, next
}

// wantNoEvents asserts a poll reported nothing.
func wantNoEvents(t *testing.T, events []notification, what string) {
	t.Helper()
	if len(events) != 0 {
		t.Fatalf("%s: got %d events, want 0", what, len(events))
	}
}

// wantOneEvent asserts a poll reported exactly one event with the given flags,
// and returns it so the caller can check its ids.
func wantOneEvent(t *testing.T, events []notification, flags uint16, what string) notification {
	t.Helper()
	if len(events) != 1 || events[0].flags != flags {
		t.Fatalf("%s: got %+v, want one event with flags %#x", what, events, flags)
	}
	return events[0]
}
