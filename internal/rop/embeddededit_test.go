package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// embeddedCarrier seeds a message carrying one encapsulated message and opens the
// chain a client walks to reach it: the message, its attachment, and the embedded
// message itself under the given open flags.
func embeddedCarrier(t *testing.T, sess *Session, logonH uint32, flags uint8) (mid int64, msgH, embH uint32) {
	t.Helper()
	store := sess.get(logonH).store
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, mid = seedCarrier(t, store, emlWithEmbedded)

	_, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), []uint32{logonH, 0xFFFFFFFF})
	msgH = h[1]
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	attH := h[1]
	oem, h := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, flags), []uint32{attH, 0xFFFFFFFF})
	ropOK(t, oem, ropOpenEmbeddedMessage, "OpenEmbeddedMessage")
	return mid, msgH, h[1]
}

// embeddedSubject re-opens a stored message's encapsulated message and returns its
// normalized subject, which is what the OpenEmbeddedMessage response carries.
func embeddedSubject(t *testing.T, sess *Session, logonH uint32, mid int64) string {
	t.Helper()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), []uint32{logonH, 0xFFFFFFFF})
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{h[1], 0xFFFFFFFF})
	oem, _ := sess.Dispatch(buildOpenEmbeddedMessage(0, 1, mapiModify), []uint32{h[1], 0xFFFFFFFF})
	p := ropOK(t, oem, ropOpenEmbeddedMessage, "OpenEmbeddedMessage(read-back)")
	mustU8(t, p, "Reserved")
	_, _ = p.Uint64() // MessageId
	mustU8(t, p, "HasNamedProperties")
	readTypedString(t, p) // SubjectPrefix
	return readTypedString(t, p)
}

// messageChangeNumber returns the stored change number of one inbox message.
func messageChangeNumber(t *testing.T, store *objectstore.Store, mid int64) uint64 {
	t.Helper()
	objs, err := store.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatalf("ListFolderObjects: %v", err)
	}
	for _, o := range objs {
		if o.ID == mid {
			return o.ChangeNumber
		}
	}
	t.Fatalf("message %d is not in the inbox", mid)
	return 0
}

// TestEditStoredEmbeddedMessagePersists is the defect this change fixes: a
// property written to an encapsulated message was accepted and then dropped, so a
// client that edited one read the old value back on the next open. An exception to
// a recurring appointment lives as an encapsulated message, so without this the
// only way to correct one field of one occurrence is to rewrite the whole series.
func TestEditStoredEmbeddedMessagePersists(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	mid, _, embH := embeddedCarrier(t, sess, logonH, mapiModify)

	sp, _ := sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Edited Inner"},
		{Tag: mapi.PrNormalizedSubject, Value: "Edited Inner"},
	}), []uint32{embH})
	ropOK(t, sp, ropSetProperties, "SetProperties(stored embedded)")

	scm, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, embH})
	ropOK(t, scm, ropSaveChangesMessage, "SaveChangesMessage(stored embedded)")

	if got := embeddedSubject(t, sess, logonH, mid); got != "Edited Inner" {
		t.Fatalf("re-opened embedded subject = %q, want Edited Inner", got)
	}
}

// TestEditStoredEmbeddedMessageAdvancesTheChangeNumber pairs with the test above.
// ICS reports a message as updated only when its change number advances, so an
// edit that leaves the counter alone reaches the store and no already-synced
// client, Outlook, ActiveSync or EWS, ever downloads it. The carrier's save is
// what advances it, the same rule an attachment add or delete follows.
func TestEditStoredEmbeddedMessageAdvancesTheChangeNumber(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	mid, msgH, embH := embeddedCarrier(t, sess, logonH, mapiModify)
	before := messageChangeNumber(t, store, mid)

	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Edited Inner"},
		{Tag: mapi.PrNormalizedSubject, Value: "Edited Inner"},
	}), []uint32{embH})
	sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, embH})
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	ropOK(t, sc, ropSaveChangesMessage, "SaveChangesMessage(carrier)")

	if after := messageChangeNumber(t, store, mid); after <= before {
		t.Fatalf("carrier change number %d did not advance past %d, so no synced client sees the edit", after, before)
	}
}

// TestReadOnlyEmbeddedMessageRefusesAWrite locks the other half: an embedded
// message opened without MAPI_MODIFY has nowhere to be saved, so the write is
// refused where it is made. Reporting success and dropping the value at the save
// leaves the client believing the edit was stored.
func TestReadOnlyEmbeddedMessageRefusesAWrite(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	mid, _, embH := embeddedCarrier(t, sess, logonH, 0)

	sp, _ := sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Should Not Stick"},
	}), []uint32{embH})
	p := ext.NewPull(sp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecAccessDenied {
		t.Fatalf("SetProperties on a read-only embedded message = %#x, want ecAccessDenied", ec)
	}
	if got := embeddedSubject(t, sess, logonH, mid); got != "Inner Subject" {
		t.Fatalf("embedded subject = %q, want the original Inner Subject", got)
	}
}
