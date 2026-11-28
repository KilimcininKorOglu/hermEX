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

// buildSaveChangesAttachment builds a RopSaveChangesAttachment request.
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
	p := ropOK(t, resp, ropCreateAttachment, "CreateAttachment")
	if h[1] == 0xFFFFFFFF {
		t.Fatal("CreateAttachment left output handle unset")
	}
	return mustU32(t, p, "AttachmentId"), h[1]
}

// openAttachmentEC drives RopOpenAttachment by number off the message at slot 0
// and returns the response's ReturnValue.
func openAttachmentEC(t *testing.T, sess *Session, msgH uint32, num uint32) uint32 {
	t.Helper()
	resp := dispatchOn(sess, buildOpenAttachment(0, 1, num), msgH, 0xFFFFFFFF)
	return readEC(t, resp, ropOpenAttachment)
}

// TestAttachmentWriteChain drives the full attachment write path through ROP:
// create two attachments on an opened message, fill the first, save it, save the
// message, then prove delete-stable numbering, change-number bump, and missing/
// invalid-handle refusals.
func TestAttachmentWriteChain(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	inboxFID := int64(mapi.PrivateFIDInbox)

	mid := uint64(seedInboxMessage(t, dir, "HOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	// Hostile: null parent handle on CreateAttachment and DeleteAttachment are refused.
	badCA, badH := sess.Dispatch(buildCreateAttachment(0, 1), []uint32{0xFFFFFFFF, 0xFFFFFFFF})
	wantEC(t, badCA, ropCreateAttachment, ecNotSupported, "CreateAttachment(null parent)")
	if len(badH) != 2 || badH[1] != 0xFFFFFFFF {
		t.Fatalf("refused CreateAttachment handles = %#x, want slot 1 unset", badH)
	}
	wantEC(t, dispatchOn(sess, buildDeleteAttachment(0, 0), 0xFFFFFFFF), ropDeleteAttachment, ecError, "DeleteAttachment(null handle)")

	pre, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: inboxFID, Given: looseSet(mid), Seen: looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cn1 := pre.LastCN

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	// First attachment: create (num 0), fill filename + data, save.
	num0, attH := createAttachmentNum(t, sess, msgH)
	if num0 != 0 {
		t.Fatalf("first attach number = %d, want 0", num0)
	}
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "w.bin"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("WDATA")},
	}), ropSetProperties, "SetProperties(attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)

	// Second attachment: create (num 1).
	num1, attH1 := createAttachmentNum(t, sess, msgH)
	if num1 != 1 || sess.get(attH1) == nil {
		t.Fatalf("second attach = (%d, %d), want (1, live handle)", num1, attH1)
	}

	// Save the message: the attachment edits dirtied it, so the change number bumps.
	saveChangesEID(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH))

	// The first attachment reads back the saved filename + data.
	wantOpenAttachment(t, sess, msgH, num0, ecSuccess, "OpenAttachment(num0) after save")
	checkSavedAttachment(t, store, mid, num0, "w.bin", "WDATA")
	assertAttachmentCount(t, store, mid, 2, "message")

	// Delete the first attachment; the second must keep number 1 (not renumber).
	dispatchOK(t, sess, buildDeleteAttachment(0, num0), ropDeleteAttachment, "DeleteAttachment", msgH)
	wantOpenAttachment(t, sess, msgH, num1, ecSuccess, "surviving attachment after sibling delete")
	wantOpenAttachment(t, sess, msgH, num0, ecNotFound, "deleted attachment")
	for _, missing := range []uint32{num0, 99} {
		wantEC(t, dispatchOn(sess, buildDeleteAttachment(0, missing), msgH), ropDeleteAttachment, ecNotFound, "DeleteAttachment(missing)")
	}
	wantOpenAttachment(t, sess, msgH, num1, ecSuccess, "survivor after deletes of missing numbers")

	assertAttachmentEditsBumpedCN(t, store, inboxFID, mid, cn1)

	surv, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	if len(surv.Attachments) != 1 {
		t.Fatalf("after delete: %d attachments, want 1", len(surv.Attachments))
	}
	wantProp(t, surv.Attachments[0].Props, mapi.PrAttachNum, int32(num1), "surviving attach number")
}

// wantOpenAttachment asserts RopOpenAttachment answers a given return code for
// one attach number.
func wantOpenAttachment(t *testing.T, sess *Session, msgH uint32, num uint32, want uint32, label string) {
	t.Helper()
	if ec := openAttachmentEC(t, sess, msgH, num); ec != want {
		t.Errorf("%s: OpenAttachment(%d) = %#x, want %#x", label, num, ec, want)
	}
}

// checkSavedAttachment asserts the stored attachment num of message mid
// carries the filename and data the client set before saving.
func checkSavedAttachment(t *testing.T, store *objectstore.Store, mid uint64, num uint32, wantName, wantData string) {
	t.Helper()
	attID, err := store.AttachmentIDByNumber(int64(mid), num)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetAttachmentProperties(attID, mapi.PrAttachLongFilename, mapi.PrAttachDataBin)
	if err != nil {
		t.Fatal(err)
	}
	if name := rowProp(t, saved, mapi.PrAttachLongFilename, "saved filename"); name != wantName {
		t.Errorf("saved filename = %v, want %s", name, wantName)
	}
	if data := rowProp(t, saved, mapi.PrAttachDataBin, "saved data"); string(asBytes(data)) != wantData {
		t.Errorf("saved data = %v, want %s", data, wantData)
	}
}

// assertAttachmentCount asserts how many attachments a stored message holds.
func assertAttachmentCount(t *testing.T, store *objectstore.Store, mid uint64, want int, label string) {
	t.Helper()
	msg, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != want {
		t.Fatalf("%s has %d attachments, want %d", label, len(msg.Attachments), want)
	}
}

// assertAttachmentEditsBumpedCN checks that an attachment-only change advanced
// the parent message's change number.
func assertAttachmentEditsBumpedCN(t *testing.T, store *objectstore.Store, folderID int64, mid, cn1 uint64) {
	t.Helper()
	post, err := store.GetContentSync(objectstore.ContentSyncRequest{
		FolderID: folderID, Given: looseSet(mid), Seen: looseSet(cn1),
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
}

// TestSaveChangesAttachmentHandleWiring locks the asymmetric handle resolution:
// the attachment must be the body InputHandleIndex and the message the
// common-header handle.
func TestSaveChangesAttachmentHandleWiring(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "HOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	// Inverted wiring must fail rather than save against the wrong object.
	inv := dispatchOn(sess, buildSaveChangesAttachment(0, 1), attH, msgH)
	if ec := readEC(t, inv, ropSaveChangesAttachment); ec == ecSuccess {
		t.Error("SaveChangesAttachment with inverted handles succeeded, want failure")
	}

	// Correct wiring succeeds afterwards.
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)
}
