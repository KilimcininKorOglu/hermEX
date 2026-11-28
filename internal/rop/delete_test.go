package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildDeletePropsOp builds a RopDeleteProperties (or NoReplicate) request: a
// PROPTAG_ARRAY of the tags to remove.
func buildDeletePropsOp(ropID, inIdx uint8, tags []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	mustBuild(b.PropTags(tags))
	return b.Bytes()
}

// TestDeleteMessageProperties drives RopDeleteProperties on an opened message:
// proves removals reach the store, a delete-after-set overrides buffered sets,
// NoReplicate behaves identically, the change number advances, and a null-handle
// delete is refused without dropping any property.
func TestDeleteMessageProperties(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)
	mid := uint64(seedInboxMessage(t, dir, "DELME"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	// Hostile: null-handle DeleteProperties returns ecError and preserves PrSubject.
	wantEC(t, dispatchOn(sess, buildDeletePropsOp(ropDeleteProperties, 0, []mapi.PropTag{mapi.PrSubject}), 0xFFFFFFFF), ropDeleteProperties, ecError, "DeleteProperties(null handle)")
	if check := mustMessageProps(t, store, int64(mid), mapi.PrSubject); stringProp(check, mapi.PrSubject) != "DELME" {
		t.Fatalf("null-handle DeleteProperties modified subject: %v", check)
	}

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	// Set PrImportance and save, so there is a persisted property to delete.
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}), ropSetProperties, "SetProperties(importance)", msgH)
	sc := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH)
	saveChangesEID(t, sc)
	before := mustMessageProps(t, store, int64(mid), mapi.PrSubject, mapi.PrImportance)
	if _, ok := before.Get(mapi.PrImportance); !ok {
		t.Fatal("PrImportance missing before delete")
	}
	cnSet := contentSync(t, store, inboxFID, mid).LastCN

	// Set PrSensitivity (buffered, unsaved), then delete subject + importance + sensitivity.
	deleted := []mapi.PropTag{mapi.PrSubject, mapi.PrImportance, mapi.PrSensitivity}
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSensitivity, Value: int32(1)}}), ropSetProperties, "SetProperties(sensitivity)", msgH)
	dp := dispatchOn(sess, buildDeletePropsOp(ropDeleteProperties, 0, deleted), msgH)
	p := ropOK(t, dp, ropDeleteProperties, "DeleteProperties")
	wantU16(t, p, "DeleteProperties PropertyProblemCount", 0)
	sc2 := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH)
	saveChangesEID(t, sc2)

	// All three are gone from the store; the buffered sensitivity set did not leak.
	after := mustMessageProps(t, store, int64(mid), deleted...)
	for _, tag := range deleted {
		if _, ok := after.Get(tag); ok {
			t.Errorf("property %s survived DeleteProperties", tag)
		}
	}

	// The delete advanced the change number beyond the set-save.
	if post := contentSync(t, store, inboxFID, mid, cnSet); post.LastCN <= cnSet {
		t.Errorf("DeleteProperties did not advance the change number: %d -> %d", cnSet, post.LastCN)
	}

	// The NoReplicate variant is accepted and reports success.
	nr := dispatchOn(sess, buildDeletePropsOp(ropDeletePropertiesNoReplicate, 0, []mapi.PropTag{mapi.PrSubject}), msgH)
	ropOK(t, nr, ropDeletePropertiesNoReplicate, "DeletePropertiesNoReplicate")
}

// TestSetAfterDeleteWins drives the reverse of the delete-after-set case: a
// property deleted then set again before save survives.
func TestSetAfterDeleteWins(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "SETWINS"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	before := mustMessageProps(t, store, int64(mid), mapi.PrImportance)
	if _, ok := before.Get(mapi.PrImportance); !ok {
		t.Fatal("PrImportance missing on the seeded message")
	}
	ropOK(t, dispatchOn(sess, buildDeletePropsOp(ropDeleteProperties, 0, []mapi.PropTag{mapi.PrImportance}), msgH), ropDeleteProperties, "DeleteProperties")
	ropOK(t, dispatchOn(sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}), msgH), ropSetProperties, "SetProperties")
	sc := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH)
	saveChangesEID(t, sc)

	after := mustMessageProps(t, store, int64(mid), mapi.PrImportance)
	v := rowProp(t, after, mapi.PrImportance, "PrImportance")
	if v != int32(2) {
		t.Errorf("PrImportance = %v, want 2 (the value set after the delete)", v)
	}
}

// storeAttachment reads the single attachment's property bag from the stored message.
func storeAttachment(t *testing.T, store *objectstore.Store, mid int64) mapi.PropertyValues {
	t.Helper()
	m, err := store.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("stored message has %d attachments, want 1", len(m.Attachments))
	}
	return m.Attachments[0].Props
}

// TestDeleteAttachmentPropertyReachesStore drives RopDeleteProperties on a created
// attachment whose properties are persisted and verifies both delete-from-store
// and set-after-delete ordering.
func TestDeleteAttachmentPropertyReachesStore(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := seedInboxMessage(t, dir, "ATTDELHOST")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	// Create an attachment, set filename + data, and save.
	_, attH := createAttachmentNum(t, sess, msgH)
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "doc.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("DATA")},
	}), ropSetProperties, "SetProperties(attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment(init)", msgH, attH)
	if fn := rowProp(t, storeAttachment(t, store, mid), mapi.PrAttachLongFilename, "persisted filename"); fn != "doc.txt" {
		t.Fatalf("persisted filename = %v, want doc.txt", fn)
	}

	// Delete the filename and save: the store row must lose it, the data stays.
	dispatchOK(t, sess, buildDeletePropsOp(ropDeleteProperties, 0, []mapi.PropTag{mapi.PrAttachLongFilename}), ropDeleteProperties, "DeleteProperties(attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment(after delete)", msgH, attH)
	att1 := storeAttachment(t, store, mid)
	if _, ok := att1.Get(mapi.PrAttachLongFilename); ok {
		t.Error("attachment filename survived DeleteProperties in the store")
	}
	if d := rowProp(t, att1, mapi.PrAttachDataBin, "attachment data"); !bytes.Equal(asBytes(d), []byte("DATA")) {
		t.Errorf("attachment data lost on filename delete: %q", asBytes(d))
	}

	// Set-after-delete: delete then set the filename in one edit; the set wins.
	dispatchOK(t, sess, buildDeletePropsOp(ropDeleteProperties, 0, []mapi.PropTag{mapi.PrAttachLongFilename}), ropDeleteProperties, "DeleteProperties(re-set)", attH)
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrAttachLongFilename, Value: "again.txt"}}), ropSetProperties, "SetProperties(re-set)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment(re-set)", msgH, attH)
	if fn := rowProp(t, storeAttachment(t, store, mid), mapi.PrAttachLongFilename, "set-after-delete filename"); fn != "again.txt" {
		t.Errorf("set-after-delete attachment filename = %v, want again.txt", fn)
	}
}
