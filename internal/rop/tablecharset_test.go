package rop

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// storeSubjectAsString8 rewrites a message's subject to the ANSI string type,
// the form an ANSI client writes and the form a FastTransfer upload carrying a
// code-page string stores.
func storeSubjectAsString8(t *testing.T, dir string, msgID int64, subject string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	props := mapi.PropertyValues{{Tag: mapi.PrSubject.WithType(mapi.PtString8), Value: subject}}
	if err := st.ModifyMessageProperties(msgID, props, mapi.PrSubject); err != nil {
		t.Fatal(err)
	}
}

// assertMIDs checks the returned rows carry exactly the given store ids, in
// order. The rows project PR_MID rather than the subject, so the assertion tests
// which rows the filter or sort chose and not how a row renders a property.
func assertMIDs(t *testing.T, rows []mapi.PropertyValues, want ...int64) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, id := range want {
		wantMID := int64(mapi.MakeEIDEx(1, uint64(id)))
		if mid, _ := rows[i].Get(mapi.PrMid); mid != wantMID {
			t.Errorf("row %d MID = %v, want %d", i, mid, wantMID)
		}
	}
}

// TestRestrictMatchesAcrossStringTypes pins that a table filter reads a property
// stored under either string type. A client's restriction names the string tag
// it prefers, and a filter that insists on the exact type drops a row whose
// property is present under the sibling tag.
func TestRestrictMatchesAcrossStringTypes(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Apple")
	bID := seedInboxMessage(t, dir, "Banana")
	seedInboxMessage(t, dir, "Cherry")
	storeSubjectAsString8(t, dir, bID, "Banana")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrMid}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	mustDispatchOK(t, sess, buildRestrict(0, propEq(mapi.PrSubject, "Banana")), []uint32{tableH}, ropRestrict)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertMIDs(t, rows, bID)
}

// TestSortOrdersAcrossStringTypes is the same guarantee for the sort key: a row
// whose subject is stored under the sibling string type must sort by its value,
// not fall to the end as a row with no key at all.
func TestSortOrdersAcrossStringTypes(t *testing.T) {
	dir := t.TempDir()
	cID := seedInboxMessage(t, dir, "Charlie")
	aID := seedInboxMessage(t, dir, "Alpha")
	bID := seedInboxMessage(t, dir, "Bravo")
	storeSubjectAsString8(t, dir, aID, "Alpha")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrMid}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)
	mustDispatchOK(t, sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), []uint32{tableH}, ropSortTable)

	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertMIDs(t, rows, aID, bID, cID)
}
