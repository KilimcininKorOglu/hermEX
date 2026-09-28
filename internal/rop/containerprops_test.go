package rop

import (
	"testing"

	"hermex/internal/mapi"
)

// TestGetPropertiesOnTheStoreAndAFolder proves RopGetPropertiesSpecific answers on
// a logon's store and on an opened folder ([MS-OXCSTOR] 2.2.2, [MS-OXCFOLD]
// 2.2.2), not only on messages: Outlook reads the folder's name and counts, and
// the store's size, through these handles.
func TestGetPropertiesOnTheStoreAndAFolder(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "x")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	cols := []mapi.PropTag{mapi.PrOOFState}
	out, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{logonH})
	ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(store)")

	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	cols = []mapi.PropTag{mapi.PrDisplayName, mapi.PrContentCount, mapi.PrContentUnreadCount}
	out, _ = sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{h[1]})
	p := ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(folder)")
	row := decodeRow(t, p, cols)
	if name, _ := row.Get(mapi.PrDisplayName); name != "Inbox" {
		t.Errorf("folder display name = %v, want Inbox", name)
	}
	// [MS-OXCFOLD] 2.2.2.2.1: the counts the store computes for what the folder holds.
	wantProp(t, row, mapi.PrContentCount, int32(1), "content count")
	wantProp(t, row, mapi.PrContentUnreadCount, int32(1), "unread count")
}
