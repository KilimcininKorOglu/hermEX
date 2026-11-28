package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestGetPropertiesOnTheStoreAndAFolder proves RopGetPropertiesSpecific answers on
// a logon's store and on an opened folder ([MS-OXCSTOR] 2.2.2, [MS-OXCFOLD]
// 2.2.2), not only on messages: Outlook reads the folder's name and counts, and
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

	// [MS-OXOOF] 2.2.5.2 PidTagOutOfOfficeState: TRUE while the reply is on.
	cols := []mapi.PropTag{mapi.PrOOFState}
	p := ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), logonH), ropGetPropertiesSpecific, "GetPropertiesSpecific(store)")
	wantProp(t, decodeRow(t, p, cols), mapi.PrOOFState, true, "store OOF state")

	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)
	cols = []mapi.PropTag{mapi.PrDisplayName, mapi.PrContentCount, mapi.PrContentUnreadCount}
	p = ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[1]), ropGetPropertiesSpecific, "GetPropertiesSpecific(folder)")
	row := decodeRow(t, p, cols)
	wantProp(t, row, mapi.PrDisplayName, "Inbox", "folder display name")
	// [MS-OXCFOLD] 2.2.2.2.1: the counts the store computes for what the folder holds.
	wantProp(t, row, mapi.PrContentCount, int32(1), "content count")
	wantProp(t, row, mapi.PrContentUnreadCount, int32(1), "unread count")
}

// TestGetPropertiesRefusesAnUnknownHandle proves RopGetPropertiesSpecific on a
// server object handle the session never issued answers an error and reads no
// object, and that the refusal leaves the store's handle answering as before.
func TestGetPropertiesRefusesAnUnknownHandle(t *testing.T) {
	dir := provisionedDir(t)
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
	wantProp(t, decodeRow(t, p, cols), mapi.PrOOFState, false, "store OOF state")
}
