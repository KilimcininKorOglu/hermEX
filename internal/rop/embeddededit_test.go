package rop

import (
	"testing"

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

	msgH = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	attH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", msgH, 0xFFFFFFFF)[1]
	embH = dispatchOK(t, sess, buildOpenEmbeddedMessage(0, 1, flags), ropOpenEmbeddedMessage, "OpenEmbeddedMessage", attH, 0xFFFFFFFF)[1]
	return mid, msgH, embH
}

// embeddedSubject re-opens a stored message's encapsulated message and returns its
// normalized subject, which is what the OpenEmbeddedMessage response carries.
func embeddedSubject(t *testing.T, sess *Session, logonH uint32, mid int64) string {
	t.Helper()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage(read-back)", logonH, 0xFFFFFFFF)[1]
	attH := dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment(read-back)", msgH, 0xFFFFFFFF)[1]
	oem := dispatchOn(sess, buildOpenEmbeddedMessage(0, 1, mapiModify), attH, 0xFFFFFFFF)
	p := ropOK(t, oem, ropOpenEmbeddedMessage, "OpenEmbeddedMessage(read-back)")
	mustU8(t, p, "Reserved")
	mustU64(t, p, "MessageId")
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
// client that edited one read the old value back on the next open.
func TestEditStoredEmbeddedMessagePersists(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	mid, _, embH := embeddedCarrier(t, sess, logonH, mapiModify)

	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Edited Inner"},
		{Tag: mapi.PrNormalizedSubject, Value: "Edited Inner"},
	}), ropSetProperties, "SetProperties(stored embedded)", embH)

	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage(stored embedded)", logonH, embH)

	if got := embeddedSubject(t, sess, logonH, mid); got != "Edited Inner" {
		t.Fatalf("re-opened embedded subject = %q, want Edited Inner", got)
	}
}

// TestEditStoredEmbeddedMessageAdvancesTheChangeNumber pairs with the test above.
// ICS reports a message as updated only when its change number advances.
func TestEditStoredEmbeddedMessageAdvancesTheChangeNumber(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	mid, msgH, embH := embeddedCarrier(t, sess, logonH, mapiModify)
	before := messageChangeNumber(t, store, mid)

	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Edited Inner"},
		{Tag: mapi.PrNormalizedSubject, Value: "Edited Inner"},
	}), ropSetProperties, "SetProperties(stored embedded)", embH)
	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage(stored embedded)", logonH, embH)
	dispatchOK(t, sess, buildSaveChangesMessage(0, 1), ropSaveChangesMessage, "SaveChangesMessage(carrier)", logonH, msgH)

	if after := messageChangeNumber(t, store, mid); after <= before {
		t.Fatalf("carrier change number %d did not advance past %d, so no synced client sees the edit", after, before)
	}
}

// TestReadOnlyEmbeddedMessageRefusesAWrite locks the other half: an embedded
// message opened without MAPI_MODIFY cannot be written or saved, and a refused
// write leaves the original inner subject untouched.
func TestReadOnlyEmbeddedMessageRefusesAWrite(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	mid, _, embH := embeddedCarrier(t, sess, logonH, 0)

	sp := dispatchOn(sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "Should Not Stick"},
	}), embH)
	wantEC(t, sp, ropSetProperties, ecAccessDenied, "SetProperties on read-only embedded message")

	wantEC(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, embH), ropSaveChangesMessage, ecNotSupported, "SaveChangesMessage on read-only embedded message")

	if got := embeddedSubject(t, sess, logonH, mid); got != "Inner Subject" {
		t.Fatalf("embedded subject = %q, want the original Inner Subject", got)
	}
}
