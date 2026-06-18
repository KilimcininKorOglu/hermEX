package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// buildCopyProperties builds a RopCopyProperties request: source at srcIdx,
// destination at dstIdx (DestHandleIndex), the copy flags, and the inclusive tag set.
func buildCopyProperties(srcIdx, dstIdx, copyFlags uint8, tags []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCopyProperties)
	b.Uint8(0) // LogonId
	b.Uint8(srcIdx)
	b.Uint8(dstIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(copyFlags)
	_ = b.PropTags(tags)
	return b.Bytes()
}

// buildCopyTo builds a RopCopyTo request: source at srcIdx, destination at dstIdx,
// the copy flags, and the excluded tag set.
func buildCopyTo(srcIdx, dstIdx, copyFlags uint8, excluded []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCopyTo)
	b.Uint8(0) // LogonId
	b.Uint8(srcIdx)
	b.Uint8(dstIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(0) // WantSubObjects
	b.Uint8(copyFlags)
	_ = b.PropTags(excluded)
	return b.Bytes()
}

// TestCopyPropertiesAndCopyTo drives RopCopyProperties and RopCopyTo from an opened
// source message to fresh compose messages. CopyProperties copies only the listed
// tag; CopyTo copies everything except the excluded tag. Both are verified by saving
// the destination and reading the stored result.
func TestCopyPropertiesAndCopyTo(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "COPYSRC"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	// Source: open and add PrImportance so there are two scalar properties to copy.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	srcH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}), []uint32{srcH})
	saveChangesEID(t, mustDispatch(sess, buildSaveChangesMessage(0, 1), logonH, srcH))

	copyOK := func(resp []byte, ropID uint8, label string) {
		p := ext.NewPull(resp, ext.FlagUTF16)
		if id := mustU8(t, p, "RopId"); id != ropID {
			t.Fatalf("%s RopId = %#x", label, id)
		}
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Fatalf("%s ReturnValue = %#x", label, ec)
		}
		if pc := mustU16(t, p, "problemCount"); pc != 0 {
			t.Errorf("%s PropertyProblemCount = %d, want 0", label, pc)
		}
	}

	// CopyProperties: copy only PrSubject into a fresh compose message.
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	dstH := h[1]
	cp, _ := sess.Dispatch(buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject}), []uint32{srcH, dstH})
	copyOK(cp, ropCopyProperties, "CopyProperties")
	dstID := int64(mapi.EID(saveChangesEID(t, mustDispatch(sess, buildSaveChangesMessage(0, 1), logonH, dstH))).GCValue())
	cpProps, _ := store.GetMessageProperties(dstID, mapi.PrSubject, mapi.PrImportance)
	if v, _ := cpProps.Get(mapi.PrSubject); v != "COPYSRC" {
		t.Errorf("CopyProperties subject = %v, want COPYSRC", v)
	}
	if _, ok := cpProps.Get(mapi.PrImportance); ok {
		t.Error("CopyProperties copied an unlisted property (importance)")
	}

	// CopyTo: copy everything except PrImportance into another compose message.
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	dst2H := h[1]
	ct, _ := sess.Dispatch(buildCopyTo(0, 1, 0, []mapi.PropTag{mapi.PrImportance}), []uint32{srcH, dst2H})
	copyOK(ct, ropCopyTo, "CopyTo")
	dst2ID := int64(mapi.EID(saveChangesEID(t, mustDispatch(sess, buildSaveChangesMessage(0, 1), logonH, dst2H))).GCValue())
	ctProps, _ := store.GetMessageProperties(dst2ID, mapi.PrSubject, mapi.PrImportance)
	if v, _ := ctProps.Get(mapi.PrSubject); v != "COPYSRC" {
		t.Errorf("CopyTo subject = %v, want COPYSRC", v)
	}
	if _, ok := ctProps.Get(mapi.PrImportance); ok {
		t.Error("CopyTo copied an excluded property (importance)")
	}
}

// TestCopyPropertiesErrors locks the refusal paths: a null destination reports
// ecDstNullObject with the destination handle index echoed, a copy between
// mismatched object categories (message to attachment) is declined, and MAPI_MOVE is
// unsupported.
func TestCopyPropertiesErrors(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "COPYERR"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	srcH := h[1]

	// Null destination: dhindex slot holds the null handle.
	nd, _ := sess.Dispatch(buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject}), []uint32{srcH, 0xFFFFFFFF})
	p := ext.NewPull(nd, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecDstNullObject {
		t.Fatalf("null-dest CopyProperties ec = %#x, want ecDstNullObject", ec)
	}
	if dh, _ := p.Uint32(); dh != 1 {
		t.Errorf("null-dest echoed DestHandleIndex = %d, want 1", dh)
	}

	// Type mismatch: a message source and an attachment destination.
	_, attH := createAttachmentNum(t, sess, srcH)
	tm, _ := sess.Dispatch(buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject}), []uint32{srcH, attH})
	p = ext.NewPull(tm, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecDeclineCopy {
		t.Errorf("mismatched-category copy ec = %#x, want ecDeclineCopy", ec)
	}

	// MAPI_MOVE is unsupported.
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	dstH := h[1]
	mv, _ := sess.Dispatch(buildCopyProperties(0, 1, mapiMove, []mapi.PropTag{mapi.PrSubject}), []uint32{srcH, dstH})
	p = ext.NewPull(mv, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Errorf("MAPI_MOVE copy ec = %#x, want ecNotSupported", ec)
	}
}

// TestCopyReflectsBufferedEdits proves CopyTo copies the source's open working
// copy: a property set on the source but not yet saved is carried into the
// destination. Reading the source from the store alone would copy the stale value.
func TestCopyReflectsBufferedEdits(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "COPYBUF"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	// Open the source and set importance to 7 without saving.
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	srcH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(7)}}), []uint32{srcH})

	// CopyTo into a fresh compose message, excluding nothing, then save it.
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	dstH := h[1]
	sess.Dispatch(buildCopyTo(0, 1, 0, nil), []uint32{srcH, dstH})
	dstID := int64(mapi.EID(saveChangesEID(t, mustDispatch(sess, buildSaveChangesMessage(0, 1), logonH, dstH))).GCValue())

	// The destination carries the buffered importance, not the source's stored 1.
	props, _ := store.GetMessageProperties(dstID, mapi.PrImportance)
	if v, ok := props.Get(mapi.PrImportance); !ok || v != int32(7) {
		t.Errorf("CopyTo importance = %v (present=%v), want the source's buffered 7", v, ok)
	}
}

// buildCopyToWS builds a RopCopyTo request with an explicit WantSubObjects byte,
// for the sub-object copy tests.
func buildCopyToWS(srcIdx, dstIdx, wantSub, copyFlags uint8, excluded []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCopyTo)
	b.Uint8(0) // LogonId
	b.Uint8(srcIdx)
	b.Uint8(dstIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(wantSub)
	b.Uint8(copyFlags)
	_ = b.PropTags(excluded)
	return b.Bytes()
}

// seedMessageWithSubObjects writes an Inbox message carrying one recipient and one
// by-value attachment (a forward scenario), so a copy of it has sub-objects to
// carry. Returns the stored message id.
func seedMessageWithSubObjects(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msg := &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: "FWDSRC"},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrDisplayName, Value: "Bob"},
			{Tag: mapi.PrEmailAddress, Value: "bob@hermex.test"},
			{Tag: mapi.PrAddrType, Value: "SMTP"},
			{Tag: mapi.PrSmtpAddress, Value: "bob@hermex.test"},
		}},
		Attachments: []oxcmail.Attachment{{Props: mapi.PropertyValues{
			{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
			{Tag: mapi.PrAttachLongFilename, Value: "note.txt"},
			{Tag: mapi.PrAttachDataBin, Value: []byte("attached-bytes")},
		}}},
	}
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), msg)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestCopyToSubObjects proves RopCopyTo carries a message's sub-objects: with
// WantSubObjects set, the recipient and the attachment (including its data) are
// copied into the compose destination; excluding PR_MESSAGE_ATTACHMENTS suppresses
// the attachment while keeping the recipient; and without WantSubObjects neither
// sub-object is copied (only scalar properties). This is the gap the earlier code
// left when it copied scalar properties only.
func TestCopyToSubObjects(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	srcID := uint64(seedMessageWithSubObjects(t, dir))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	// Open the source, CopyTo into a fresh compose message, save it, and return the
	// stored destination message.
	copyInto := func(wantSub uint8, excluded []mapi.PropTag) *oxcmail.Message {
		t.Helper()
		_, hh := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, srcID))), []uint32{logonH, 0xFFFFFFFF})
		sH := hh[1]
		_, hh = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
		dH := hh[1]
		resp, _ := sess.Dispatch(buildCopyToWS(0, 1, wantSub, 0, excluded), []uint32{sH, dH})
		p := ext.NewPull(resp, ext.FlagUTF16)
		if id := mustU8(t, p, "RopId"); id != ropCopyTo {
			t.Fatalf("CopyTo RopId = %#x", id)
		}
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Fatalf("CopyTo ReturnValue = %#x", ec)
		}
		dstID := int64(mapi.EID(saveChangesEID(t, mustDispatch(sess, buildSaveChangesMessage(0, 1), logonH, dH))).GCValue())
		m, err := store.OpenMessage(dstID)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	// WantSubObjects: recipient and attachment (with data) both copied.
	full := copyInto(1, nil)
	if len(full.Recipients) != 1 {
		t.Fatalf("recipients copied = %d, want 1", len(full.Recipients))
	}
	if e, _ := full.Recipients[0].Get(mapi.PrSmtpAddress); e != "bob@hermex.test" {
		t.Errorf("copied recipient smtp = %v, want bob@hermex.test", e)
	}
	if len(full.Attachments) != 1 {
		t.Fatalf("attachments copied = %d, want 1", len(full.Attachments))
	}
	if data, _ := full.Attachments[0].Props.Get(mapi.PrAttachDataBin); !bytes.Equal(asBytes(data), []byte("attached-bytes")) {
		t.Errorf("copied attachment data = %q, want attached-bytes", asBytes(data))
	}

	// Exclude PR_MESSAGE_ATTACHMENTS: recipient still copied, attachment suppressed.
	noAtt := copyInto(1, []mapi.PropTag{mapi.PrMessageAttachments})
	if len(noAtt.Recipients) != 1 {
		t.Errorf("recipients with attachments excluded = %d, want 1", len(noAtt.Recipients))
	}
	if len(noAtt.Attachments) != 0 {
		t.Errorf("attachment not suppressed by exclude = %d, want 0", len(noAtt.Attachments))
	}

	// WantSubObjects FALSE: no sub-objects, but scalar properties still copied.
	none := copyInto(0, nil)
	if len(none.Recipients) != 0 || len(none.Attachments) != 0 {
		t.Errorf("sub-objects copied without WantSubObjects: recips=%d attachs=%d", len(none.Recipients), len(none.Attachments))
	}
	if subj, _ := none.Props.Get(mapi.PrSubject); subj != "FWDSRC" {
		t.Errorf("scalar subject not copied = %v, want FWDSRC", subj)
	}
}

// asBytes coerces a property value to its byte slice (PtBinary), or nil.
func asBytes(v any) []byte {
	b, _ := v.([]byte)
	return b
}

// mustDispatch dispatches a single ROP with a two-slot handle array and returns the
// response bytes — a small helper for the save steps in the copy tests.
func mustDispatch(sess *Session, rop []byte, h0, h1 uint32) []byte {
	resp, _ := sess.Dispatch(rop, []uint32{h0, h1})
	return resp
}
