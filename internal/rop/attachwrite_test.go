package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildCreateAttachment builds a RopCreateAttachment request (OutputHandleIndex).
func buildCreateAttachment(inIdx, outIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCreateAttachment)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	return b.Bytes()
}

// buildSaveChangesAttachment builds a RopSaveChangesAttachment request. respIdx is
// the common-header handle (the parent message); attIdx is the body
// InputHandleIndex (the attachment) — the asymmetric wiring the handler relies on.
func buildSaveChangesAttachment(respIdx, attIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSaveChangesAttachment)
	b.Uint8(0) // LogonId
	b.Uint8(respIdx)
	b.Uint8(attIdx) // ihindex2
	b.Uint8(0)      // SaveFlags
	return b.Bytes()
}

// buildDeleteAttachment builds a RopDeleteAttachment request (AttachmentId).
func buildDeleteAttachment(inIdx uint8, attachID uint32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropDeleteAttachment)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint32(attachID)
	return b.Bytes()
}

// createAttachmentNum drives one RopCreateAttachment off the message at handle
// slot 0 and returns the assigned attach number plus the new attachment handle.
func createAttachmentNum(t *testing.T, sess *Session, msgH uint32) (uint32, uint32) {
	t.Helper()
	resp, h := sess.Dispatch(buildCreateAttachment(0, 1), []uint32{msgH, 0xFFFFFFFF})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropCreateAttachment {
		t.Fatalf("CreateAttachment RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CreateAttachment ReturnValue = %#x", ec)
	}
	return mustU32(t, p, "AttachmentId"), h[1]
}

// openAttachmentEC drives RopOpenAttachment by number off the message at slot 0
// and returns the response's ReturnValue.
func openAttachmentEC(t *testing.T, sess *Session, msgH uint32, num uint32) uint32 {
	t.Helper()
	resp, _ := sess.Dispatch(buildOpenAttachment(0, 1, num), []uint32{msgH, 0xFFFFFFFF})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	return mustU32(t, p, "ec")
}

// TestAttachmentWriteChain drives the full attachment write path through ROP:
// create two attachments on an opened message, fill the first, save it, save the
// message, then prove the delete-stable numbering and the change-number bump.
// Deleting the first attachment must leave the second resolvable at its original
// number, and the message — changed only by attachment edits, with no top-level
// property set — must still surface as updated in the ICS content-sync diff.
func TestAttachmentWriteChain(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	mid := uint64(seedInboxMessage(t, dir, "HOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	// The message's create change number, for the later updated-in-sync assertion.
	pre, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cn1 := pre.LastCN

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// First attachment: create (num 0), fill filename + data, save.
	num0, attH := createAttachmentNum(t, sess, msgH)
	if num0 != 0 {
		t.Fatalf("first attach number = %d, want 0", num0)
	}
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "w.bin"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("WDATA")},
	}), []uint32{attH})
	// SaveChangesAttachment: message at the header slot (0), attachment at ihindex2 (1).
	scA, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	p := ext.NewPull(scA, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSaveChangesAttachment {
		t.Fatalf("SaveChangesAttachment RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SaveChangesAttachment ReturnValue = %#x", ec)
	}

	// Second attachment: create (num 1).
	num1, _ := createAttachmentNum(t, sess, msgH)
	if num1 != 1 {
		t.Fatalf("second attach number = %d, want 1", num1)
	}

	// Save the message: the attachment edits dirtied it, so the change number bumps.
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	saveChangesEID(t, sc)

	// The first attachment reads back the saved filename + data.
	if ec := openAttachmentEC(t, sess, msgH, num0); ec != ecSuccess {
		t.Fatalf("OpenAttachment(num0) after save = %#x", ec)
	}
	saved, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Attachments) != 2 {
		t.Fatalf("message has %d attachments, want 2", len(saved.Attachments))
	}

	// Delete the first attachment; the second must keep number 1 (not renumber).
	del, _ := sess.Dispatch(buildDeleteAttachment(0, num0), []uint32{msgH})
	p = ext.NewPull(del, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropDeleteAttachment {
		t.Fatalf("DeleteAttachment RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("DeleteAttachment ReturnValue = %#x", ec)
	}
	if ec := openAttachmentEC(t, sess, msgH, num1); ec != ecSuccess {
		t.Errorf("surviving attachment not resolvable at num %d after sibling delete: %#x", num1, ec)
	}
	if ec := openAttachmentEC(t, sess, msgH, num0); ec != ecNotFound {
		t.Errorf("deleted attachment OpenAttachment(num0) = %#x, want ecNotFound", ec)
	}

	// The attachment-only change advanced the message change number, so the message
	// surfaces as updated against the pre-change sync state.
	post, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(cn1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if post.LastCN <= cn1 {
		t.Errorf("attachment edits did not advance the change number: was %d, now %d", cn1, post.LastCN)
	}
	if !containsMID(post.UpdatedMIDs, mid) {
		t.Errorf("message with attachment edits missing from UpdatedMIDs: %v", post.UpdatedMIDs)
	}

	// White-box: the surviving attachment's stored data is intact.
	surv, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	if len(surv.Attachments) != 1 {
		t.Fatalf("after delete: %d attachments, want 1", len(surv.Attachments))
	}
	if v, _ := surv.Attachments[0].Props.Get(mapi.PrAttachNum); v != int32(num1) {
		t.Errorf("surviving attach number = %v, want %d", v, num1)
	}
}

// TestSaveChangesAttachmentHandleWiring locks the asymmetric handle resolution:
// the attachment must be the body InputHandleIndex and the message the
// common-header handle. Inverting them (message at ihindex2, attachment at the
// header) must fail rather than silently save against the wrong object.
func TestSaveChangesAttachmentHandleWiring(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "HOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	// Inverted wiring: message handle at ihindex2 (slot 1), attachment at the header
	// (slot 0). The handler resolves ihindex2 as the attachment, finds the message
	// there, and must reject.
	inv, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{attH, msgH})
	p := ext.NewPull(inv, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec == ecSuccess {
		t.Error("SaveChangesAttachment with inverted handles succeeded, want failure")
	}

	// Correct wiring still works for the same handles.
	ok, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	p = ext.NewPull(ok, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Errorf("SaveChangesAttachment with correct handles = %#x, want success", ec)
	}
}
