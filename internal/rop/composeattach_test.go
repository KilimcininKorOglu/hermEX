package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
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
// surviving attachment keeps its original (non-renumbered) attach number — the
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
	p := ext.NewPull(del, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("DeleteAttachment(pre-save) ReturnValue = %#x", ec)
	}

	// Save the composed message: its one surviving attachment is written with it.
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	savedID := int64(mapi.EID(saveChangesEID(t, sc)).GCValue())

	// White-box: exactly the surviving attachment persisted, at its original number,
	// with its filename and payload intact through the store's content offload.
	saved, err := store.OpenMessage(savedID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Attachments) != 1 {
		t.Fatalf("composed message has %d attachments, want 1 (the pre-save delete)", len(saved.Attachments))
	}
	got := saved.Attachments[0].Props
	if v, _ := got.Get(mapi.PrAttachNum); v != int32(num1) {
		t.Errorf("surviving attach number = %v, want %d (pre-save delete must not renumber)", v, num1)
	}
	if v, _ := got.Get(mapi.PrAttachLongFilename); v != "second.bin" {
		t.Errorf("surviving attachment filename = %v, want second.bin", v)
	}
	if v, ok := got.Get(mapi.PrAttachDataBin); !ok {
		t.Error("surviving attachment lost its payload")
	} else if vb, _ := v.([]byte); !bytes.Equal(vb, []byte("SECONDDATA")) {
		t.Errorf("surviving attachment data = %q, want SECONDDATA", vb)
	}

	// Black-box through the ROP read path: the saved message re-opens and the
	// surviving attachment resolves at num1 while the deleted one is gone.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(savedID)))), []uint32{logonH, 0xFFFFFFFF})
	reH := h[1]
	if ec := openAttachmentEC(t, sess, reH, num1); ec != ecSuccess {
		t.Errorf("OpenAttachment(num1) on saved compose message = %#x, want success", ec)
	}
	if ec := openAttachmentEC(t, sess, reH, num0); ec != ecNotFound {
		t.Errorf("OpenAttachment(num0=deleted) = %#x, want ecNotFound", ec)
	}
}
