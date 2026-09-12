package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// These two tests pin what a contents table's row snapshot is for, because the
// snapshot is about to be made cheaper and the change must not move either
// property.
//
// A row's values come from the store at QueryRows time, not from the snapshot, so
// the snapshot need carry no property values at all. The row SET, on the other
// hand, is frozen when the table is built: RopQueryPosition, RopSeekRow,
// RopSeekRowBookmark and RopFindRow all address rows by index, so a table that
// re-read the folder would silently move a client's cursor and bookmarks under it.

// TestContentsTableRowReadsTheStore proves a row is projected from the store when
// QueryRows asks for it. An edit made after the table was built shows through.
func TestContentsTableRowReadsTheStore(t *testing.T) {
	dir := t.TempDir()
	mid := seedInboxMessage(t, dir, "Before")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	// Edit the message after the table exists. A second open of the same mailbox
	// takes the shared lock the session already holds.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var props mapi.PropertyValues
	oxcmail.SetSubject(&props, "After")
	if err := st.ModifyMessageProperties(mid, props); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	// The edit moved the folder, so the next Execute carries a TABLE_CHANGED for
	// the open table. Drain it here, both to assert it and to leave the QueryRows
	// response below carrying nothing but its own rows.
	wake, _ := sess.Dispatch(nil, nil)
	wantTableChanged(t, wake, tableH)

	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "After")
}

// TestContentsTableSnapshotIsFrozen proves the row set is fixed when the table is
// built. A message delivered afterwards is neither counted nor returned, so the
// row indices a client already holds keep naming the same rows.
func TestContentsTableSnapshotIsFrozen(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "One")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	// Delivered after the table was built.
	seedInboxMessage(t, dir, "Two")

	assertTablePosition(t, sess, tableH, "after a later delivery", 0, 1)

	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "One")
}
