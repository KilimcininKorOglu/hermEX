package rop

import (
	"slices"
	"testing"

	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// looseSet builds a loose, home-replica-keyed idset over single values, the
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
	p := ropOK(t, resp, ropSaveChangesMessage, "SaveChangesMessage")
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

// TestInPlaceModifyBumpsChangeNumber proves editing an existing message in place
// advances its change number so GetContentSync reports it as an update, while an
// unauthorized delegate write is refused and preserves both the subject and LastCN.
func TestInPlaceModifyBumpsChangeNumber(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	mid := uint64(seedInboxMessage(t, dir, "ORIG"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	cn1 := contentSync(t, store, inboxFID, mid).LastCN
	if cn1 == 0 {
		t.Fatal("create did not assign a change number")
	}
	if base := contentSync(t, store, inboxFID, mid, cn1); containsMID(base.ChangedMIDs, mid) {
		t.Fatalf("message reported changed before any edit (seen=cn1=%d)", cn1)
	}

	assertModifyRefusalsPreserveCN(t, sess, store, logonH, inboxFID, mid, cn1)

	// In-place modify: open the existing message for edit, set a new subject, save.
	openedH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	if obj := sess.get(openedH); obj == nil || obj.kind != kindMessage {
		t.Fatalf("opened-message object wrong kind: %+v", obj)
	}
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "EDITED"}}), ropSetProperties, "SetProperties", openedH)
	sc := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, openedH)
	if got := uint64(mapi.EID(saveChangesEID(t, sc)).GCValue()); got != mid {
		t.Fatalf("SaveChangesMessage on edit returned message id %d, want %d", got, mid)
	}

	// The edit must advance the change number and surface as an UPDATE against cn1.
	post := contentSync(t, store, inboxFID, mid, cn1)
	if post.LastCN <= cn1 {
		t.Errorf("change number did not advance on in-place edit: was %d, now %d", cn1, post.LastCN)
	}
	if !containsMID(post.ChangedMIDs, mid) {
		t.Errorf("edited message missing from ChangedMIDs: %v", post.ChangedMIDs)
	}
	if !containsMID(post.UpdatedMIDs, mid) {
		t.Errorf("edited message missing from UpdatedMIDs: %v", post.UpdatedMIDs)
	}

	props, err := store.GetMessageProperties(int64(mid), mapi.PrSubject)
	if err != nil {
		t.Fatal(err)
	}
	wantProp(t, props, mapi.PrSubject, "EDITED", "in-place subject")
}

// contentSync runs one content-sync request for a single message.
func contentSync(t *testing.T, store *objectstore.Store, folderID int64, mid uint64, seen ...uint64) objectstore.ContentSyncResult {
	t.Helper()
	res, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: folderID, Given: looseSet(mid), Seen: looseSet(seen...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assertModifyRefusalsPreserveCN(t *testing.T, sess *Session, store *objectstore.Store, logonH uint32, inboxFID int64, mid, cn1 uint64) {
	t.Helper()
	wantEC(t, dispatchOn(sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "HACKED"}}), 0xFFFFFFFF), ropSetProperties, ecError, "SetProperties(null handle)")
	wantEC(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, logonH), ropSaveChangesMessage, ecError, "SaveChangesMessage(logon handle)")
	if unchanged := contentSync(t, store, inboxFID, mid, cn1); len(unchanged.ChangedMIDs) != 0 || unchanged.LastCN != cn1 {
		t.Fatalf("refused writes altered sync state: %+v", unchanged)
	}
}

// TestOpenSaveNoEditKeepsChangeNumber guards the no-spurious-bump rule: opening a
// message and saving it without any SetProperties must leave the change number
// untouched.
func TestOpenSaveNoEditKeepsChangeNumber(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	mid := uint64(seedInboxMessage(t, dir, "ORIG"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	pre, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cn1 := pre.LastCN

	openedH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	sc := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, openedH)
	saveChangesEID(t, sc)

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
