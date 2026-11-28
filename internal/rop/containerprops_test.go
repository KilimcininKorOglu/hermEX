package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestGetPropertiesOnTheStoreAndAFolder proves RopGetPropertiesSpecific answers on
// a logon's store and on an opened folder ([MS-OXCROPS] RopGetPropertiesSpecific
// takes any property-bearing object), not only on messages: Outlook reads the folder's name and counts, and
// the store's size, through these handles.
func TestGetPropertiesOnTheStoreAndAFolder(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "x")
	// Turn the Out of Office reply on, so the store property read below must come
	// from the mailbox and not from a default.
	st, err := objectstore.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetOOFSettings(objectstore.OOFSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	// PidTagOutOfOfficeState ([MS-OXOOF]): TRUE while the reply is on.
	cols := []mapi.PropTag{mapi.PrOOFState}
	p := ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), logonH), ropGetPropertiesSpecific, "GetPropertiesSpecific(store)")
	wantProp(t, decodeRow(t, p, cols), mapi.PrOOFState, true, "store OOF state")

	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)
	cols = []mapi.PropTag{mapi.PrDisplayName, mapi.PrContentCount, mapi.PrContentUnreadCount}
	p = ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[1]), ropGetPropertiesSpecific, "GetPropertiesSpecific(folder)")
	row := decodeRow(t, p, cols)
	wantProp(t, row, mapi.PrDisplayName, "Inbox", "folder display name")
	// PidTagContentCount and PidTagContentUnreadCount ([MS-OXCFOLD]): the folder
	// holds one seeded message, and it is unread.
	wantProp(t, row, mapi.PrContentCount, int32(1), "content count")
	wantProp(t, row, mapi.PrContentUnreadCount, int32(1), "unread count")
}

// TestGetPropertiesRefusesAnUnknownHandle proves RopGetPropertiesSpecific on a
// server object handle the session never issued answers an error and reads no
// object, and that the refusal leaves the store's handle answering as before.
func TestGetPropertiesRefusesAnUnknownHandle(t *testing.T) {
	dir := provisionedDir(t)
	// A stored, non-default OOF state, so the read after the refusal proves the
	// mailbox kept it rather than falling back to a default.
	st, err := objectstore.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetOOFSettings(objectstore.OOFSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	cols := []mapi.PropTag{mapi.PrOOFState}
	bogus := logonH + 1000
	resp, out := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{bogus})
	wantEC(t, resp, ropGetPropertiesSpecific, ecError, "GetPropertiesSpecific on an unknown handle")
	if len(out) != 1 || out[0] != bogus || sess.get(bogus) != nil {
		t.Errorf("handle table after the refusal = %v, want [%d] with no object behind it", out, bogus)
	}

	p := ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), logonH), ropGetPropertiesSpecific, "GetPropertiesSpecific(store) after the refusal")
	wantProp(t, decodeRow(t, p, cols), mapi.PrOOFState, true, "store OOF state after the refusal")
}
