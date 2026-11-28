package rop

import (
	"testing"

	"hermex/internal/mapi"
)

// TestHierarchyRestrictOnComputedCount proves a hierarchy table filters on a
// folder property the store computes: a view of the folders holding unread mail
// finds the Inbox with one unread message and not the empty folders beside it.
func TestHierarchyRestrictOnComputedCount(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "unread")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	ipmEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, ipmEID), ropOpenFolder, "OpenFolder", h[0], 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildGetHierarchyTable(0, 1), ropGetHierarchyTable, "GetHierarchyTable", h[1], 0xFFFFFFFF)
	tableH := h[1]
	cols := []mapi.PropTag{mapi.PrDisplayName}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	hasUnread := &mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopGT, PropTag: mapi.PrContentUnreadCount,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrContentUnreadCount, Value: int32(0)},
	}}
	mustDispatchOK(t, sess, buildRestrict(0, hasUnread), []uint32{tableH}, ropRestrict)
	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 64), tableH)
	origin, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("folders with unread mail = %d rows, want 1", len(rows))
	}
	if origin != 2 {
		t.Errorf("QueryRows Origin = %d, want 2 (END)", origin)
	}
	if name, ok := rows[0].Get(mapi.PrDisplayName); !ok || name != "Inbox" {
		t.Errorf("folder with unread mail = %v, want Inbox", name)
	}
}
