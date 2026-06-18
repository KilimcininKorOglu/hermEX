package rop

import (
	"slices"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// looseSet builds a loose, home-replica-keyed idset over single values — the
// form GetContentSync compares a client's prior synchronization state against.
func looseSet(vs ...uint64) *ics.IDSet {
	s := ics.NewIDSet(ics.FormIDLoose, nil)
	for _, v := range vs {
		s.AppendRange(1, v, v)
	}
	return s
}

// saveChangesEID parses a RopSaveChangesMessage response and returns the echoed
// MessageId EID, failing the test on a malformed or error response.
func saveChangesEID(t *testing.T, resp []byte) uint64 {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSaveChangesMessage {
		t.Fatalf("SaveChangesMessage RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SaveChangesMessage ReturnValue = %#x", ec)
	}
	mustU8(t, p, "ihindex2")
	eid, err := p.Uint64()
	if err != nil {
		t.Fatalf("SaveChangesMessage MessageId: %v", err)
	}
	return eid
}

// containsMID reports whether mid appears in a delta MID list.
func containsMID(mids []uint64, mid uint64) bool {
	return slices.Contains(mids, mid)
}

// TestInPlaceModifyBumpsChangeNumber is the B.Inc 4-defer-b unblock: editing an
// existing message in place through the ROP write path (OpenMessage →
// SetProperties → SaveChangesMessage) must reallocate the message's change
// number so the ICS content-sync diff reports it as an UPDATE, not leave it
// looking unchanged. A props-readback alone would not prove the unblock — the
// load-bearing assertion is that GetContentSync, run against the state the client
// held before the edit, now lists the message in both ChangedMIDs and
// UpdatedMIDs with an advanced high-water change number.
func TestInPlaceModifyBumpsChangeNumber(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	mid := uint64(seedInboxMessage(t, dir, "ORIG"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	// Read the message's create change number via a sync with an empty Seen set:
	// unacknowledged, the message is in the delta and LastCN is its create CN.
	pre, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cn1 := pre.LastCN
	if cn1 == 0 {
		t.Fatal("create did not assign a change number")
	}

	// With the create CN acknowledged, the message is up to date — not in the delta.
	base, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(cn1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsMID(base.ChangedMIDs, mid) {
		t.Fatalf("message reported changed before any edit (seen=cn1=%d)", cn1)
	}

	// In-place modify: open the existing message for edit, set a new subject, save.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	openedH := h[1]
	if obj := sess.get(openedH); obj == nil || obj.kind != kindMessage {
		t.Fatalf("opened-message object wrong kind: %+v", obj)
	}
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "EDITED"}}), []uint32{openedH})
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, openedH})
	if got := uint64(mapi.EID(saveChangesEID(t, sc)).GCValue()); got != mid {
		t.Fatalf("SaveChangesMessage on edit returned message id %d, want %d", got, mid)
	}

	// The edit must advance the change number and surface as an UPDATE against the
	// pre-edit client state.
	post, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(cn1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if post.LastCN <= cn1 {
		t.Errorf("change number did not advance on in-place edit: was %d, now %d", cn1, post.LastCN)
	}
	if !containsMID(post.ChangedMIDs, mid) {
		t.Errorf("edited message missing from ChangedMIDs: %v", post.ChangedMIDs)
	}
	if !containsMID(post.UpdatedMIDs, mid) {
		t.Errorf("edited message missing from UpdatedMIDs (the B.Inc 4-defer-b unblock): %v", post.UpdatedMIDs)
	}

	// The new property value persisted in place (not a duplicate insert).
	props, err := store.GetMessageProperties(int64(mid), mapi.PrSubject)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrSubject); v != "EDITED" {
		t.Errorf("in-place subject = %v, want EDITED", v)
	}
}

// TestOpenSaveNoEditKeepsChangeNumber guards the no-spurious-bump rule: opening a
// message and saving it without any SetProperties must leave the change number
// untouched (the reference's !b_touched early-out). A bump here would make every
// read-only open-then-save re-sync the message to every client.
func TestOpenSaveNoEditKeepsChangeNumber(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	mid := uint64(seedInboxMessage(t, dir, "ORIG"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	pre, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cn1 := pre.LastCN

	// Open then save with no property change.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	openedH := h[1]
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, openedH})
	saveChangesEID(t, sc) // asserts success

	post, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(cn1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if post.LastCN != cn1 {
		t.Errorf("no-edit save changed the high-water change number: was %d, now %d", cn1, post.LastCN)
	}
	if containsMID(post.ChangedMIDs, mid) {
		t.Errorf("no-edit save reported the message as changed: %v", post.ChangedMIDs)
	}
}
