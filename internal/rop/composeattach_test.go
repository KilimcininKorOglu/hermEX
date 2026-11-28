package rop

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// fillAndSaveAttachment fills a created attachment (handle attH) with a filename
// and payload via RopSetProperties, then persists it with RopSaveChangesAttachment
// (header handle = parent message msgH, body ihindex2 = attachment attH).
func fillAndSaveAttachment(t *testing.T, sess *Session, msgH, attH uint32, name string, data []byte) {
	t.Helper()
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: name},
		{Tag: mapi.PrAttachDataBin, Value: data},
	}), ropSetProperties, "SetProperties(attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)
}

// TestComposeMessageWithAttachment drives the everyday compose-with-attachment
// flow entirely against a not-yet-saved message: CreateMessage opens an in-memory
// message, CreateAttachment stages attachments on it before any save,
// SaveChangesAttachment buffers each payload, one attachment is deleted before the
// message is saved, and SaveChangesMessage writes the message with its surviving
// attachment in a single CreateMessage. It also verifies deleting a non-existent
// attachment number is refused without disturbing surviving attachments.
func TestComposeMessageWithAttachment(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	// Compose a new message (in memory, no store row yet).
	msgH := dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]
	if obj := sess.get(msgH); obj == nil || obj.kind != kindNewMessage {
		t.Fatalf("compose message object wrong: %+v", obj)
	}
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "COMPOSED"}}), ropSetProperties, "SetProperties(msg)", msgH)

	// Two attachments staged before any save.
	num0, attH0 := createAttachmentNum(t, sess, msgH)
	if num0 != 0 {
		t.Fatalf("first compose attach number = %d, want 0", num0)
	}
	fillAndSaveAttachment(t, sess, msgH, attH0, "first.bin", []byte("FIRST"))

	num1, attH1 := createAttachmentNum(t, sess, msgH)
	if num1 != 1 {
		t.Fatalf("second compose attach number = %d, want 1", num1)
	}
	fillAndSaveAttachment(t, sess, msgH, attH1, "second.bin", []byte("SECONDDATA"))

	// Drop the first attachment before the message is ever saved; the second must
	// keep its number rather than slide down to 0, and deleting an already-deleted
	// or out-of-range attachment must be refused cleanly.
	dispatchOK(t, sess, buildDeleteAttachment(0, num0), ropDeleteAttachment, "DeleteAttachment(pre-save)", msgH)
	wantEC(t, dispatchOn(sess, buildDeleteAttachment(0, num0), msgH), ropDeleteAttachment, ecNotFound, "DeleteAttachment(already deleted)")
	wantEC(t, dispatchOn(sess, buildDeleteAttachment(0, 999), msgH), ropDeleteAttachment, ecNotFound, "DeleteAttachment(missing)")

	// Save the composed message: its one surviving attachment is written with it.
	sc := dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH)
	savedID := int64(mapi.EID(saveChangesEID(t, sc)).GCValue())

	assertSurvivingAttachment(t, store, savedID, num1)

	// Black-box through the ROP read path: the saved message re-opens and the
	// surviving attachment resolves at num1 while the deleted one is gone.
	reH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(savedID)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	wantOpenAttachment(t, sess, reH, num1, ecSuccess, "saved compose message")
	wantOpenAttachment(t, sess, reH, num0, ecNotFound, "the pre-save deleted attachment")
}

// assertSurvivingAttachment checks the one attachment a pre-save delete left
// behind: it persisted at its original number, with its filename and payload
// intact through the store's content offload.
func assertSurvivingAttachment(t *testing.T, store *objectstore.Store, savedID int64, num1 uint32) {
	t.Helper()
	saved, err := store.OpenMessage(savedID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Attachments) != 1 {
		t.Fatalf("composed message has %d attachments, want 1 (the pre-save delete)", len(saved.Attachments))
	}
	got := saved.Attachments[0].Props
	wantProp(t, got, mapi.PrAttachNum, int32(num1), "surviving attach number (a pre-save delete must not renumber)")
	wantProp(t, got, mapi.PrAttachLongFilename, "second.bin", "surviving attachment filename")
	v := rowProp(t, got, mapi.PrAttachDataBin, "surviving attachment data")
	vb, ok := v.([]byte)
	if !ok {
		t.Fatalf("surviving attachment data = %T, want []byte", v)
	}
	if !bytes.Equal(vb, []byte("SECONDDATA")) {
		t.Errorf("surviving attachment data = %q, want SECONDDATA", vb)
	}
}
