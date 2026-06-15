package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildQueryRows builds a RopQueryRows request (QueryRowsFlags, ForwardRead,
// RowCount) over the table at the given input handle index.
func buildQueryRows(inIdx, queryFlags, forwardRead uint8, rowCount uint16) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropQueryRows)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(queryFlags)
	b.Uint8(forwardRead)
	b.Uint16(rowCount)
	return b.Bytes()
}

// buildGetHierarchyTable builds a RopGetHierarchyTable request.
func buildGetHierarchyTable(inIdx, outIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetHierarchyTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(0) // TableFlags
	return b.Bytes()
}

// decodeRow reads one PROPERTY_ROW for the given column set: a NONE row yields a
// bare value per column, a FLAGGED row a FLAGGED_PROPVAL (only available values
// are returned). It mirrors the reader half of buildPropertyRow.
func decodeRow(t *testing.T, p *ext.Pull, cols []mapi.PropTag) mapi.PropertyValues {
	t.Helper()
	var pv mapi.PropertyValues
	flag := mustU8(t, p, "rowFlag")
	for _, col := range cols {
		if flag == propertyRowNone {
			v, err := p.PropValue(col.Type())
			if err != nil {
				t.Fatalf("PropValue %#x: %v", uint32(col), err)
			}
			pv.Set(col, v)
		} else {
			fv, err := p.FlaggedPropVal(col.Type())
			if err != nil {
				t.Fatalf("FlaggedPropVal %#x: %v", uint32(col), err)
			}
			if fv.Flag == mapi.FlaggedAvailable {
				pv.Set(col, fv.Value)
			}
		}
	}
	return pv
}

// TestBuildPropertyRowNone confirms the NONE form: when every column is present
// the row is the 0x00 flag followed by a bare value per column, in order.
func TestBuildPropertyRowNone(t *testing.T) {
	cols := []mapi.PropTag{mapi.PrSubject, mapi.PrMessageFlags}
	var props mapi.PropertyValues
	props.Set(mapi.PrSubject, "Hi")
	props.Set(mapi.PrMessageFlags, int32(0x09))

	out := ext.NewPush(ext.FlagUTF16)
	if err := buildPropertyRow(out, cols, props); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if b[0] != propertyRowNone {
		t.Fatalf("row flag = %#x, want NONE (%#x)", b[0], propertyRowNone)
	}
	p := ext.NewPull(b[1:], ext.FlagUTF16)
	if subj, _ := p.PropValue(mapi.PrSubject.Type()); subj != "Hi" {
		t.Errorf("column 0 = %v, want \"Hi\"", subj)
	}
	if fl, _ := p.PropValue(mapi.PrMessageFlags.Type()); fl != int32(0x09) {
		t.Errorf("column 1 = %v, want 9", fl)
	}
	if p.Remaining() != 0 {
		t.Errorf("NONE row has trailing bytes: %d (no per-column flags expected)", p.Remaining())
	}
}

// TestBuildPropertyRowFlagged confirms the FLAGGED form: a missing column flips
// the row to 0x01 and each column carries a FLAGGED_PROPVAL — 0x00 + value when
// present, a bare 0x01 when absent.
func TestBuildPropertyRowFlagged(t *testing.T) {
	cols := []mapi.PropTag{mapi.PrSubject, mapi.PrMessageDeliveryTime}
	var props mapi.PropertyValues
	props.Set(mapi.PrSubject, "Hi") // PrMessageDeliveryTime deliberately absent

	out := ext.NewPush(ext.FlagUTF16)
	if err := buildPropertyRow(out, cols, props); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if b[0] != propertyRowFlagged {
		t.Fatalf("row flag = %#x, want FLAGGED (%#x)", b[0], propertyRowFlagged)
	}
	p := ext.NewPull(b[1:], ext.FlagUTF16)
	fv0, err := p.FlaggedPropVal(mapi.PrSubject.Type())
	if err != nil {
		t.Fatal(err)
	}
	if fv0.Flag != mapi.FlaggedAvailable || fv0.Value != "Hi" {
		t.Errorf("column 0 flagged = (%#x, %v), want (available, \"Hi\")", fv0.Flag, fv0.Value)
	}
	fv1, err := p.FlaggedPropVal(mapi.PrMessageDeliveryTime.Type())
	if err != nil {
		t.Fatal(err)
	}
	if fv1.Flag != mapi.FlaggedUnavailable {
		t.Errorf("column 1 flag = %#x, want unavailable (%#x)", fv1.Flag, mapi.FlaggedUnavailable)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after FLAGGED row: %d", p.Remaining())
	}
}

// queryRowsResponse parses a QueryRows response down to its rows: header
// (RopId, hindex, ec), then SeekPosition + RowCount, returning the cursor and
// the decoded rows for the column set.
func queryRowsResponse(t *testing.T, resp []byte, cols []mapi.PropTag) (seekPos uint8, rows []mapi.PropertyValues) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropQueryRows {
		t.Fatalf("RopId = %#x, want QueryRows", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("QueryRows ReturnValue = %#x", ec)
	}
	seekPos = mustU8(t, p, "seekPos")
	count := mustU16(t, p, "count")
	for i := 0; i < int(count); i++ {
		rows = append(rows, decodeRow(t, p, cols))
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after %d rows: %d", count, p.Remaining())
	}
	return seekPos, rows
}

// TestQueryRowsContents seeds one message and walks Logon -> OpenFolder ->
// GetContentsTable -> SetColumns -> QueryRows, asserting the row carries the
// seeded subject and the cursor reports end-of-table.
func TestQueryRowsContents(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: sender@hermex.test\r\nTo: alice@hermex.test\r\n" +
		"Subject: KEYSTONE-ROW\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess := NewSession(dir, nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	gct, h := sess.Dispatch(buildGetContentsTable(0, 1), []uint32{folderH, 0xFFFFFFFF})
	if rc := contentsRowCount(t, gct); rc != 1 {
		t.Fatalf("GetContentsTable RowCount = %d, want 1", rc)
	}
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrSubject}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})

	seekPos, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("QueryRows returned %d rows, want 1", len(rows))
	}
	if subj, _ := rows[0].Get(mapi.PrSubject); subj != "KEYSTONE-ROW" {
		t.Errorf("row subject = %v, want \"KEYSTONE-ROW\"", subj)
	}
	if seekPos != bookmarkEnd {
		t.Errorf("seek position = %#x, want END (%#x) after reading the last row", seekPos, bookmarkEnd)
	}
}

// contentsRowCount extracts the RowCount from a GetContentsTable response.
func contentsRowCount(t *testing.T, resp []byte) uint32 {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	mustU32(t, p, "ec")
	return mustU32(t, p, "RowCount")
}

// TestQueryRowsHierarchy walks Logon -> OpenFolder(IPM subtree) ->
// GetHierarchyTable -> SetColumns -> QueryRows and confirms the child folder
// rows include the Inbox by display name.
func TestQueryRowsHierarchy(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	ipmEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDIPMSubtree))
	_, h = sess.Dispatch(buildOpenFolder(0, 1, ipmEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	ght, h := sess.Dispatch(buildGetHierarchyTable(0, 1), []uint32{folderH, 0xFFFFFFFF})
	if rc := contentsRowCount(t, ght); rc == 0 {
		t.Fatal("GetHierarchyTable RowCount = 0, want the seeded child folders")
	}
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrDisplayName}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 64), []uint32{tableH})

	_, rows := queryRowsResponse(t, qr, cols)
	var found bool
	for _, r := range rows {
		if name, _ := r.Get(mapi.PrDisplayName); name == "Inbox" {
			found = true
		}
	}
	if !found {
		t.Errorf("hierarchy rows did not include Inbox (%d rows)", len(rows))
	}
}

// sortOrderEntry is one SortOrder for buildSortTable.
type sortOrderEntry struct {
	tag   mapi.PropTag
	order uint8
}

// buildSortTable builds a RopSortTable request: TableFlags, SortOrderCount,
// CategoryCount, ExpandedCount, then a PropertyType/PropertyId/Order per key.
func buildSortTable(inIdx uint8, catCount, expanded uint16, keys []sortOrderEntry) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSortTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(0)                  // TableFlags
	b.Uint16(uint16(len(keys))) // SortOrderCount
	b.Uint16(catCount)
	b.Uint16(expanded)
	for _, k := range keys {
		b.Uint16(uint16(k.tag.Type()))
		b.Uint16(k.tag.ID())
		b.Uint8(k.order)
	}
	return b.Bytes()
}

// openInboxContentsTable walks Logon -> OpenFolder(Inbox) -> GetContentsTable and
// returns the contents-table handle.
func openInboxContentsTable(t *testing.T, sess *Session) uint32 {
	t.Helper()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	_, h = sess.Dispatch(buildGetContentsTable(0, 1), []uint32{folderH, 0xFFFFFFFF})
	return h[1]
}

// assertSubjects checks the rows' PR_SUBJECT values match want, in order.
func assertSubjects(t *testing.T, rows []mapi.PropertyValues, want ...string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if subj, _ := rows[i].Get(mapi.PrSubject); subj != w {
			t.Errorf("row %d subject = %v, want %q", i, subj, w)
		}
	}
}

// TestSortTableOrdersContents pins the correctness fix: RopSortTable must actually
// reorder the rows QueryRows returns (it previously parsed the order and discarded
// it, returning store order). The messages are delivered out of alphabetical order
// so store order differs from sorted order, and a descending re-sort must replace
// the ascending one and re-page from the top.
func TestSortTableOrdersContents(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Charlie")
	seedInboxMessage(t, dir, "Alpha")
	seedInboxMessage(t, dir, "Bravo")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	mustDispatchOK(t, sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), []uint32{tableH}, ropSortTable)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Alpha", "Bravo", "Charlie")

	mustDispatchOK(t, sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortDescend}}), []uint32{tableH}, ropSortTable)
	qr, _ = sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows = queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Charlie", "Bravo", "Alpha")
}

// TestSortTableSortsOnNonDisplayedProperty guards the trap that the sort key need
// not be a displayed column: the table sorts by PR_SUBJECT while projecting only
// PR_MID, so the returned ids must come back in subject order.
func TestSortTableSortsOnNonDisplayedProperty(t *testing.T) {
	dir := t.TempDir()
	cID := seedInboxMessage(t, dir, "Charlie")
	aID := seedInboxMessage(t, dir, "Alpha")
	bID := seedInboxMessage(t, dir, "Bravo")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrMid}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)
	mustDispatchOK(t, sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), []uint32{tableH}, ropSortTable)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)

	wantMIDs := []int64{
		int64(mapi.MakeEIDEx(1, uint64(aID))),
		int64(mapi.MakeEIDEx(1, uint64(bID))),
		int64(mapi.MakeEIDEx(1, uint64(cID))),
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for i, want := range wantMIDs {
		mid, _ := rows[i].Get(mapi.PrMid)
		if mid != want {
			t.Errorf("row %d MID = %v, want %d (subject-sorted order)", i, mid, want)
		}
	}
}

// TestSortTableRejectsCategorized confirms a categorized sort fails loud rather
// than silently returning a flattened table — the same silent-error class the
// non-categorized fix closes.
func TestSortTableRejectsCategorized(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "x")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)

	resp, _ := sess.Dispatch(buildSortTable(0, 1, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), []uint32{tableH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSortTable {
		t.Fatalf("RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Errorf("categorized SortTable ec = %#x, want ecNotSupported (%#x)", ec, ecNotSupported)
	}
}

// buildRestrict builds a RopRestrict request carrying r (nil clears the filter,
// sending a zero-length restriction).
func buildRestrict(inIdx uint8, r *mapi.Restriction) []byte {
	var data []byte
	if r != nil {
		rd := ext.NewPush(ext.FlagUTF16)
		_ = rd.Restriction(*r)
		data = rd.Bytes()
	}
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropRestrict)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(0) // RestrictFlags
	b.Uint16(uint16(len(data)))
	b.Raw(data)
	return b.Bytes()
}

// propEq is a PR_SUBJECT-style equality PropertyRestriction.
func propEq(tag mapi.PropTag, val any) *mapi.Restriction {
	return &mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopEQ, PropTag: tag, PropVal: mapi.TaggedPropVal{Tag: tag, Value: val},
	}}
}

// TestRestrictFiltersByProperty pins the correctness fix: RopRestrict must filter
// the rows QueryRows returns (it previously consumed the restriction and returned
// every row). A second restriction must widen back to the full base before
// re-filtering, and an empty restriction must restore every row.
func TestRestrictFiltersByProperty(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Apple")
	seedInboxMessage(t, dir, "Banana")
	seedInboxMessage(t, dir, "Cherry")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	mustDispatchOK(t, sess, buildRestrict(0, propEq(mapi.PrSubject, "Banana")), []uint32{tableH}, ropRestrict)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Banana")

	mustDispatchOK(t, sess, buildRestrict(0, propEq(mapi.PrSubject, "Cherry")), []uint32{tableH}, ropRestrict)
	qr, _ = sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows = queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Cherry")

	mustDispatchOK(t, sess, buildRestrict(0, nil), []uint32{tableH}, ropRestrict)
	qr, _ = sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows = queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Apple", "Banana", "Cherry")
}

// TestRestrictContentSubstring covers a case-insensitive substring content match.
func TestRestrictContentSubstring(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Weekly Report A")
	seedInboxMessage(t, dir, "Summary")
	seedInboxMessage(t, dir, "report B") // lowercase, matched via IGNORECASE
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	content := &mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		FuzzyLevel: fuzzySubString | fuzzyIgnoreCase, PropTag: mapi.PrSubject,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: "report"},
	}}
	mustDispatchOK(t, sess, buildRestrict(0, content), []uint32{tableH}, ropRestrict)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Weekly Report A", "report B")
}

// TestRestrictNot covers the boolean tree: NOT(subject == Banana) keeps the rest.
func TestRestrictNot(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Apple")
	seedInboxMessage(t, dir, "Banana")
	seedInboxMessage(t, dir, "Cherry")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	not := &mapi.Restriction{Type: mapi.ResNot, Value: *propEq(mapi.PrSubject, "Banana")}
	mustDispatchOK(t, sess, buildRestrict(0, not), []uint32{tableH}, ropRestrict)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Apple", "Cherry")
}

// TestRestrictRejectsUnsupported confirms a restriction outside the v1 subset (here
// a regular-expression relop) fails loud rather than returning an unfiltered table.
func TestRestrictRejectsUnsupported(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "x")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)

	re := &mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopRE, PropTag: mapi.PrSubject,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: "x"},
	}}
	resp, _ := sess.Dispatch(buildRestrict(0, re), []uint32{tableH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropRestrict {
		t.Fatalf("RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Errorf("unsupported relop ec = %#x, want ecNotSupported (%#x)", ec, ecNotSupported)
	}
}

// TestRestrictThenSort confirms rebuildView filters before sorting: the surviving
// rows come back in sorted order, the filtered-out row absent.
func TestRestrictThenSort(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "box")
	seedInboxMessage(t, dir, "apple") // no 'x' -> filtered out
	seedInboxMessage(t, dir, "fox")
	seedInboxMessage(t, dir, "axe")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	content := &mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		FuzzyLevel: fuzzySubString, PropTag: mapi.PrSubject,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: "x"},
	}}
	mustDispatchOK(t, sess, buildRestrict(0, content), []uint32{tableH}, ropRestrict)
	mustDispatchOK(t, sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortAscend}}), []uint32{tableH}, ropSortTable)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "axe", "box", "fox")
}
