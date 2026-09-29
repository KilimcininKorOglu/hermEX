package rop

import (
	"testing"

	"hermex/internal/mapi"
)

// TestHierarchyRestrictOnComputedCount proves a hierarchy table filters on a
// folder property the store computes: a view of the folders holding unread mail
// finds the Inbox with one unread message and not the empty folders beside it.
func TestHierarchyRestrictOnComputedCount(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "unread")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	ipmEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))
	_, h = sess.Dispatch(buildOpenFolder(0, 1, ipmEID), []uint32{h[0], 0xFFFFFFFF})
	_, h = sess.Dispatch(buildGetHierarchyTable(0, 1), []uint32{h[1], 0xFFFFFFFF})
	tableH := h[1]
	cols := []mapi.PropTag{mapi.PrDisplayName}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	hasUnread := &mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopGT, PropTag: mapi.PrContentUnreadCount,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrContentUnreadCount, Value: int32(0)},
	}}
	mustDispatchOK(t, sess, buildRestrict(0, hasUnread), []uint32{tableH}, ropRestrict)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 64), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("folders with unread mail = %d rows, want 1", len(rows))
	}
	if name, _ := rows[0].Get(mapi.PrDisplayName); name != "Inbox" {
		t.Errorf("folder with unread mail = %v, want Inbox", name)
	}
}
