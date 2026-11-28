package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// These tests pin what a contents table's row snapshot is for, because the
// snapshot is about to be made cheaper and the change must not move either
// property.
//
// A row's values come from the store at QueryRows time, not from the snapshot, so
// the snapshot need carry no property values at all. The row SET, on the other
// hand, is frozen when the table is built: RopQueryPosition, RopSeekRow,
// RopSeekRowBookmark and RopFindRow all address rows by index, so a table that
// re-read the folder would silently move a client's cursor and bookmarks under it.
//
// Origin values are the predefined bookmarks of [MS-OXCTABL] 2.2.2.1.1. Section
// 3.2.5.5 and the worked example in 4.4.2 say which one a read answers: a forward
// read that leaves no more rows answers BOOKMARK_END, any other forward read
// BOOKMARK_CURRENT.
const (
	originCurrent uint8 = 0x01
	originEnd     uint8 = 0x02
	// specTblstatComplete is TBLSTAT_COMPLETE ([MS-OXCTABL] 2.2.2.1.3): no
	// operation is in progress on the table. It is spelled out here rather than
	// taken from the production constant, so a wrong constant cannot pass.
	specTblstatComplete uint8 = 0x00
)

// setContentsColumns sets cols on tableH and checks the whole RopSetColumns
// answer: success, a TBLSTAT_COMPLETE status, and the handle table unchanged.
func setContentsColumns(t *testing.T, sess *Session, tableH uint32, cols []mapi.PropTag) {
	t.Helper()
	h, p := mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)
	if len(h) != 1 || h[0] != tableH {
		t.Fatalf("SetColumns handle table = %v, want [%d]", h, tableH)
	}
	if status := mustU8(t, p, "TableStatus"); status != specTblstatComplete {
		t.Fatalf("SetColumns TableStatus = %#x, want TBLSTAT_COMPLETE", status)
	}
}

// queryOneRow sends a forward RopQueryRows for one row on tableH, checks the
// Origin the response carries, and requires exactly one row back.
func queryOneRow(t *testing.T, sess *Session, tableH uint32, cols []mapi.PropTag, wantOrigin uint8) mapi.PropertyValues {
	t.Helper()
	origin, rows := queryRowsResponse(t, dispatchOn(sess, buildQueryRows(0, 0, 1, 1), tableH), cols)
	if origin != wantOrigin {
		t.Fatalf("QueryRows Origin = %#x, want %#x", origin, wantOrigin)
	}
	if len(rows) != 1 {
		t.Fatalf("QueryRows returned %d rows, want 1", len(rows))
	}
	return rows[0]
}

// midEID is the PidTagMid a row carries for the store message id mid.
func midEID(mid int64) int64 {
	return int64(mapi.MakeEIDEx(1, uint64(mid)))
}

// TestContentsTableRowReadsTheStore proves a row is projected from the store when
// QueryRows asks for it. An edit made after the table was built shows through.
func TestContentsTableRowReadsTheStore(t *testing.T) {
	dir := provisionedDir(t)
	mid := seedInboxMessage(t, dir, "Before")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject, mapi.PrMid}
	setContentsColumns(t, sess, tableH, cols)

	// Edit the message after the table exists. A second open of the same mailbox
	// takes the shared lock the session already holds.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var props mapi.PropertyValues
	oxcmail.SetSubject(&props, "After")
	if err := st.ModifyMessageProperties(mid, props); err != nil {
		mustClose(t, st)
		t.Fatal(err)
	}
	mustClose(t, st)

	// The edit moved the folder, so the next Execute carries a TABLE_CHANGED for
	// the open table. Drain it here, both to assert it and to leave the QueryRows
	// response below carrying nothing but its own rows.
	wantTableChanged(t, dispatchOn(sess, nil), tableH)

	row := queryOneRow(t, sess, tableH, cols, originEnd)
	wantProp(t, row, mapi.PrMid, midEID(mid), "edited row id")
	wantProp(t, row, mapi.PrSubject, "After", "edited row subject")
}

// TestContentsTableSnapshotIsFrozen proves the row set is fixed when the table is
// built. A message delivered afterwards is neither counted nor returned, so the
// row indices a client already holds keep naming the same rows.
func TestContentsTableSnapshotIsFrozen(t *testing.T) {
	dir := provisionedDir(t)
	oneMID := seedInboxMessage(t, dir, "One")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject, mapi.PrMid}
	setContentsColumns(t, sess, tableH, cols)

	// Delivered after the table was built.
	if twoMID := seedInboxMessage(t, dir, "Two"); twoMID == oneMID {
		t.Fatalf("the later delivery reused message id %d", oneMID)
	}

	assertTablePosition(t, sess, tableH, "after a later delivery", 0, 1)

	row := queryOneRow(t, sess, tableH, cols, originEnd)
	wantProp(t, row, mapi.PrMid, midEID(oneMID), "snapshot row id")
	wantProp(t, row, mapi.PrSubject, "One", "snapshot row subject")
}

// TestContentsTableQueryRowsRefusesATableNeverIssued sends RopQueryRows on a
// table handle the session never issued. The ROP fails, registers no object, and
// leaves the real table's cursor where the client's last read put it: the next
// read on the real table returns the one row not read yet.
func TestContentsTableQueryRowsRefusesATableNeverIssued(t *testing.T) {
	dir := provisionedDir(t)
	firstMID := seedInboxMessage(t, dir, "First")
	secondMID := seedInboxMessage(t, dir, "Second")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject, mapi.PrMid}
	setContentsColumns(t, sess, tableH, cols)

	assertTablePosition(t, sess, tableH, "on open", 0, 2)
	// Read one of the two rows, so the cursor stands at a non-default position.
	read := queryOneRow(t, sess, tableH, cols, originCurrent)
	assertTablePosition(t, sess, tableH, "before the refusal", 1, 2)

	bogus := tableH + 1000
	resp, out := sess.Dispatch(buildQueryRows(0, 0, 1, 1), []uint32{bogus})
	wantEC(t, resp, ropQueryRows, ecError, "QueryRows on a table never issued")
	if len(out) != 1 || out[0] != bogus || sess.get(bogus) != nil {
		t.Errorf("handle table after the refusal = %v, want [%d] with no object behind it", out, bogus)
	}

	assertTablePosition(t, sess, tableH, "after the refusal", 1, 2)
	rest := queryOneRow(t, sess, tableH, cols, originEnd)
	// [MS-OXCTABL] 3.1.5: with no RopSortTable the row order is undefined, so the
	// two reads are checked as a set: each message exactly once.
	readMID, restMID := firstMID, secondMID
	if got, ok := read.Get(mapi.PrMid); ok && got == midEID(secondMID) {
		readMID, restMID = secondMID, firstMID
	}
	wantProp(t, read, mapi.PrMid, midEID(readMID), "row read before the refusal")
	wantProp(t, rest, mapi.PrMid, midEID(restMID), "row read after the refusal")
}
