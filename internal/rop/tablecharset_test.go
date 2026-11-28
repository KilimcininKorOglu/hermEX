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
	defer mustClose(t, st)
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
		wantProp(t, rows[i], mapi.PrMid, wantMID, "row MID")
	}
}

// TestRestrictMatchesAcrossStringTypes pins that a table filter reads a property
// stored under either string type. A client's restriction names the string tag
// it prefers, and a filter that insists on the exact type drops a row whose
// property is present under the sibling tag.
func TestRestrictMatchesAcrossStringTypes(t *testing.T) {
	dir := provisionedDir(t)
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
	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH)
	origin, rows := queryRowsResponse(t, qr, cols)
	if origin != bookmarkEnd {
		t.Errorf("QueryRows Origin = %d, want END (%d)", origin, bookmarkEnd)
	}
	assertMIDs(t, rows, bID)
}

// TestSortOrdersAcrossStringTypes is the same guarantee for the sort key: a row
// whose subject is stored under the sibling string type must sort by its value,
// not fall to the end as a row with no key at all.
func TestSortOrdersAcrossStringTypes(t *testing.T) {
	dir := provisionedDir(t)
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

	qr := dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH)
	origin, rows := queryRowsResponse(t, qr, cols)
	if origin != bookmarkEnd {
		t.Errorf("QueryRows Origin = %d, want END (%d)", origin, bookmarkEnd)
	}
	assertMIDs(t, rows, aID, bID, cID)
}

// TestTableCharsetRefusesUnsupportedRestrictionAndKeepsView proves a hostile
// unsupported restriction on a PtString8-backed table fails with ecNotSupported,
// preserves the prior restricted view, and leaves the stored PtString8 subject
// untouched.
func TestTableCharsetRefusesUnsupportedRestrictionAndKeepsView(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "Apple")
	bID := seedInboxMessage(t, dir, "Banana")
	storeSubjectAsString8(t, dir, bID, "Banana")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrMid}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)
	mustDispatchOK(t, sess, buildRestrict(0, propEq(mapi.PrSubject, "Banana")), []uint32{tableH}, ropRestrict)

	re := &mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopRE, PropTag: mapi.PrSubject,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: "Ban.*"},
	}}
	resp := dispatchOn(sess, buildRestrict(0, re), tableH)
	if ec := readEC(t, resp, ropRestrict); ec != ecNotSupported {
		t.Errorf("RelopRE Restrict ec = %#x, want ecNotSupported (%#x)", ec, ecNotSupported)
	}

	origin, rows := queryRowsResponse(t, dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH), cols)
	if origin != bookmarkEnd {
		t.Errorf("QueryRows Origin = %d, want END (%d)", origin, bookmarkEnd)
	}
	assertMIDs(t, rows, bID)

	st, err := objectstore.Open(dir)
	mustNil(t, err)
	defer mustClose(t, st)
	props, err := st.GetMessageProperties(bID, mapi.PrSubject.WithType(mapi.PtString8))
	mustNil(t, err)
	wantProp(t, props, mapi.PrSubject.WithType(mapi.PtString8), "Banana", "stored PtString8 subject")
}
