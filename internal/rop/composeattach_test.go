package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// fillAndSaveAttachment fills a created attachment (handle attH) with a filename
// and payload via RopSetProperties, then persists it with RopSaveChangesAttachment
// (header handle = parent message msgH, body ihindex2 = attachment attH).
func fillAndSaveAttachment(t *testing.T, sess *Session, msgH, attH uint32, name string, data []byte) {
	t.Helper()
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: name},
		{Tag: mapi.PrAttachDataBin, Value: data},
	}), []uint32{attH})
	resp, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSaveChangesAttachment {
		t.Fatalf("SaveChangesAttachment RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SaveChangesAttachment ReturnValue = %#x", ec)
	}
}

// TestComposeMessageWithAttachment drives the everyday compose-with-attachment
// flow entirely against a not-yet-saved message: CreateMessage opens an in-memory
// message, CreateAttachment stages attachments on it before any save,
// SaveChangesAttachment buffers each payload, one attachment is deleted before the
// message is saved, and SaveChangesMessage writes the message with its surviving
// attachment in a single CreateMessage. It proves the staged attachment persists
// with its filename and data, that the pre-save delete is honoured, and that the
// surviving attachment keeps its original (non-renumbered) attach number, the
// guarantee that makes a client's AttachmentId stable across a sibling delete.
func TestComposeMessageWithAttachment(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	// Compose a new message (in memory, no store row yet).
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	if obj := sess.get(msgH); obj == nil || obj.kind != kindNewMessage {
		t.Fatalf("compose message object wrong: %+v", obj)
	}
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "COMPOSED"}}), []uint32{msgH})

	// Two attachments staged before any save: CreateAttachment on an unsaved compose
	// message must now succeed (it returned ecNotSupported before this path existed).
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
	// keep its number rather than slide down to 0.
	del, _ := sess.Dispatch(buildDeleteAttachment(0, num0), []uint32{msgH})
	ropOK(t, del, ropDeleteAttachment, "DeleteAttachment(pre-save)")

	// Save the composed message: its one surviving attachment is written with it.
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	savedID := int64(mapi.EID(saveChangesEID(t, sc)).GCValue())

	assertSurvivingAttachment(t, store, savedID, num1)

	// Black-box through the ROP read path: the saved message re-opens and the
	// surviving attachment resolves at num1 while the deleted one is gone.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(savedID)))), []uint32{logonH, 0xFFFFFFFF})
	reH := h[1]
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
	v, ok := got.Get(mapi.PrAttachDataBin)
	if !ok {
		t.Fatal("surviving attachment lost its payload")
	}
	if vb, _ := v.([]byte); !bytes.Equal(vb, []byte("SECONDDATA")) {
		t.Errorf("surviving attachment data = %q, want SECONDDATA", vb)
	}
}
