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
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	inbox := int64(mapi.PrivateFIDInbox)
	wantFolder := uint64(mapi.MakeEIDEx(1, uint64(inbox)))

	// Empty inbox: no changes, empty snapshot.
	events, snap, err := detectContentChanges(st, inbox, nil)
	if err != nil {
		t.Fatalf("detect baseline: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("baseline: got %d events, want 0", len(events))
	}

	// Append → one ObjectCreated for the new message, with wire EIDs.
	raw := []byte("From: a@test\r\nTo: b@test\r\nSubject: x\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(inbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	wantMsg := uint64(mapi.MakeEIDEx(1, uint64(info.ID)))

	events, snap, err = detectContentChanges(st, inbox, snap)
	if err != nil {
		t.Fatalf("detect create: %v", err)
	}
	if len(events) != 1 || events[0].flags != fnevObjectCreated|nfByMessage {
		t.Fatalf("create: got %+v, want one ObjectCreated|byMessage", events)
	}
	if events[0].folderID != wantFolder || events[0].messageID != wantMsg {
		t.Errorf("create eids: folder=%#x msg=%#x, want folder=%#x msg=%#x",
			events[0].folderID, events[0].messageID, wantFolder, wantMsg)
	}

	// Read-state flip advances read_cn, not change_number — the MAX-based snapshot
	// must still see it as a modify (the discriminating check for the read_cn fix).
	if err := st.SetMessageReadState(info.ID, true); err != nil {
		t.Fatalf("set read state: %v", err)
	}
	events, snap, err = detectContentChanges(st, inbox, snap)
	if err != nil {
		t.Fatalf("detect modify: %v", err)
	}
	if len(events) != 1 || events[0].flags != fnevObjectModified|nfByMessage {
		t.Fatalf("read-state modify: got %+v, want one ObjectModified|byMessage", events)
	}

	// A poll with no further change emits nothing.
	events, snap, err = detectContentChanges(st, inbox, snap)
	if err != nil {
		t.Fatalf("detect idle: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("idle: got %d events, want 0", len(events))
	}

	// Delete → one ObjectDeleted carrying the gone message's id.
	if err := st.DeleteMessage(inbox, info.UID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	events, _, err = detectContentChanges(st, inbox, snap)
	if err != nil {
		t.Fatalf("detect delete: %v", err)
	}
	if len(events) != 1 || events[0].flags != fnevObjectDeleted|nfByMessage {
		t.Fatalf("delete: got %+v, want one ObjectDeleted|byMessage", events)
	}
	if events[0].messageID != wantMsg {
		t.Errorf("delete msg eid = %#x, want %#x", events[0].messageID, wantMsg)
	}
}
