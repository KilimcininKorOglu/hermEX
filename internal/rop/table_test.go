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

// TestTableStatusOps exercises the status/position/columns/abort/bookmark family
// over a 3-row contents table: GetStatus reports complete, QueryColumnsAll echoes the
// display column set, QueryPosition tracks the cursor, SeekRowFractional moves it to a
// fraction of the total, Abort fails (nothing async to abort), GetCollapseState is
// unsupported on a flat table, and FreeBookmark genuinely drops the bookmark (a
// later SeekRowBookmark on the freed blob reports ecNotFound).
func TestTableStatusOps(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "One")
	seedInboxMessage(t, dir, "Two")
	seedInboxMessage(t, dir, "Three")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	// GetStatus: a synchronously-built table is always complete.
	gs, _ := sess.Dispatch(toROPRequest(ropGetStatus, 0, nil), []uint32{tableH})
	p := ext.NewPull(gs, ext.FlagUTF16)
	mustU8(t, p, "GS.RopId")
	mustU8(t, p, "GS.hindex")
	if ec := mustU32(t, p, "GS.ec"); ec != ecSuccess {
		t.Fatalf("GetStatus ec = %#x", ec)
	}
	if st := mustU8(t, p, "GS.status"); st != tableStatusComplete {
		t.Errorf("GetStatus status = %#x, want %#x", st, tableStatusComplete)
	}

	// QueryColumnsAll: the display column set (PR_SUBJECT).
	qc, _ := sess.Dispatch(toROPRequest(ropQueryColumnsAll, 0, nil), []uint32{tableH})
	p = ext.NewPull(qc, ext.FlagUTF16)
	mustU8(t, p, "QC.RopId")
	mustU8(t, p, "QC.hindex")
	if ec := mustU32(t, p, "QC.ec"); ec != ecSuccess {
		t.Fatalf("QueryColumnsAll ec = %#x", ec)
	}
	gotCols, err := p.PropTags()
	if err != nil {
		t.Fatalf("QueryColumnsAll PropTags: %v", err)
	}
	if len(gotCols) != 1 || gotCols[0] != mapi.PrSubject {
		t.Errorf("QueryColumnsAll columns = %v, want [PrSubject]", gotCols)
	}

	// QueryPosition: cursor at the start, denominator = row count.
	assertPosition := func(label string, wantNum, wantDen uint32) {
		t.Helper()
		qp, _ := sess.Dispatch(toROPRequest(ropQueryPosition, 0, nil), []uint32{tableH})
		pp := ext.NewPull(qp, ext.FlagUTF16)
		mustU8(t, pp, "QP.RopId")
		mustU8(t, pp, "QP.hindex")
		if ec := mustU32(t, pp, "QP.ec"); ec != ecSuccess {
			t.Fatalf("%s QueryPosition ec = %#x", label, ec)
		}
		if num := mustU32(t, pp, "QP.num"); num != wantNum {
			t.Errorf("%s Numerator = %d, want %d", label, num, wantNum)
		}
		if den := mustU32(t, pp, "QP.den"); den != wantDen {
			t.Errorf("%s Denominator = %d, want %d", label, den, wantDen)
		}
	}
	assertPosition("initial", 0, 3)

	// SeekRowFractional 1/2 of 3 rows lands the cursor on row 1.
	srf := ext.NewPush(ext.FlagUTF16)
	srf.Uint32(1) // Numerator
	srf.Uint32(2) // Denominator
	sr, _ := sess.Dispatch(toROPRequest(ropSeekRowFractional, 0, srf.Bytes()), []uint32{tableH})
	if ec := readEC(t, sr, ropSeekRowFractional); ec != ecSuccess {
		t.Fatalf("SeekRowFractional ec = %#x", ec)
	}
	assertPosition("after-seek", 1, 3)

	// A zero denominator is an invalid bookmark.
	srf0 := ext.NewPush(ext.FlagUTF16)
	srf0.Uint32(1)
	srf0.Uint32(0)
	sr0, _ := sess.Dispatch(toROPRequest(ropSeekRowFractional, 0, srf0.Bytes()), []uint32{tableH})
	if ec := readEC(t, sr0, ropSeekRowFractional); ec != ecInvalidBookmark {
		t.Errorf("SeekRowFractional(den=0) ec = %#x, want ecInvalidBookmark", ec)
	}

	// Abort: there is no asynchronous build to abort.
	ab, _ := sess.Dispatch(toROPRequest(ropAbort, 0, nil), []uint32{tableH})
	if ec := readEC(t, ab, ropAbort); ec != ecUnableToAbort {
		t.Errorf("Abort ec = %#x, want ecUnableToAbort", ec)
	}

	// GetCollapseState: unsupported on a flat (uncategorized) table.
	gcs := ext.NewPush(ext.FlagUTF16)
	gcs.Uint64(0) // RowId
	gcs.Uint32(0) // RowInstanceNumber
	gc, _ := sess.Dispatch(toROPRequest(ropGetCollapseState, 0, gcs.Bytes()), []uint32{tableH})
	if ec := readEC(t, gc, ropGetCollapseState); ec != ecNotSupported {
		t.Errorf("GetCollapseState ec = %#x, want ecNotSupported", ec)
	}

	// FreeBookmark must actually drop the bookmark: create one at the current cursor,
	// free it, then a SeekRowBookmark on the freed blob reports ecNotFound.
	cb, _ := sess.Dispatch(toROPRequest(ropCreateBookmark, 0, nil), []uint32{tableH})
	pcb := ext.NewPull(cb, ext.FlagUTF16)
	mustU8(t, pcb, "CB.RopId")
	mustU8(t, pcb, "CB.hindex")
	if ec := mustU32(t, pcb, "CB.ec"); ec != ecSuccess {
		t.Fatalf("CreateBookmark ec = %#x", ec)
	}
	bookmark, err := pcb.BinShort()
	if err != nil {
		t.Fatalf("CreateBookmark bookmark: %v", err)
	}

	fb := ext.NewPush(ext.FlagUTF16)
	_ = fb.BinShort(bookmark)
	free, _ := sess.Dispatch(toROPRequest(ropFreeBookmark, 0, fb.Bytes()), []uint32{tableH})
	if ec := readEC(t, free, ropFreeBookmark); ec != ecSuccess {
		t.Fatalf("FreeBookmark ec = %#x", ec)
	}

	srb := ext.NewPush(ext.FlagUTF16)
	_ = srb.BinShort(bookmark)
	srb.Uint32(0) // Offset
	srb.Uint8(0)  // WantRowMovedCount
	seek, _ := sess.Dispatch(toROPRequest(ropSeekRowBookmark, 0, srb.Bytes()), []uint32{tableH})
	if ec := readEC(t, seek, ropSeekRowBookmark); ec != ecNotFound {
		t.Errorf("SeekRowBookmark on freed bookmark ec = %#x, want ecNotFound", ec)
	}
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

// buildSeekRow builds a RopSeekRow request (Origin, signed Offset, WantRowMovedCount).
func buildSeekRow(inIdx, seekPos uint8, offset int32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSeekRow)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(seekPos)
	b.Uint32(uint32(offset))
	b.Uint8(0) // WantRowMovedCount
	return b.Bytes()
}

// buildResetTable builds a RopResetTable request (no body).
func buildResetTable(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropResetTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	return b.Bytes()
}

// seekRowResponse parses a SeekRow response: HasSoughtLess + the signed RowsSought.
func seekRowResponse(t *testing.T, resp []byte) (hasSoughtLess uint8, sought int32) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSeekRow {
		t.Fatalf("RopId = %#x, want SeekRow", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SeekRow ec = %#x", ec)
	}
	hasSoughtLess = mustU8(t, p, "hasSoughtLess")
	sought = int32(mustU32(t, p, "offsetSought"))
	return hasSoughtLess, sought
}

// TestSeekRow moves the cursor forward from the beginning and confirms the next
// QueryRows pages from the sought position.
func TestSeekRow(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"m0", "m1", "m2", "m3", "m4"} {
		seedInboxMessage(t, dir, s)
	}
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	resp, _ := sess.Dispatch(buildSeekRow(0, bookmarkBeginning, 2), []uint32{tableH})
	if hl, sought := seekRowResponse(t, resp); hl != 0 || sought != 2 {
		t.Fatalf("SeekRow(+2 from start) = (hasSoughtLess %d, sought %d), want (0, 2)", hl, sought)
	}
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "m2", "m3", "m4")
}

// TestSeekRowClampsAtEnd confirms a seek past the end stops at the last row and
// reports HasSoughtLess.
func TestSeekRowClampsAtEnd(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"a", "b", "c"} {
		seedInboxMessage(t, dir, s)
	}
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	mustDispatchOK(t, sess, buildSetColumns(0, []mapi.PropTag{mapi.PrSubject}), []uint32{tableH}, ropSetColumns)

	resp, _ := sess.Dispatch(buildSeekRow(0, bookmarkBeginning, 100), []uint32{tableH})
	if hl, sought := seekRowResponse(t, resp); hl != 1 || sought != 3 {
		t.Errorf("SeekRow(+100) = (hasSoughtLess %d, sought %d), want (1, 3)", hl, sought)
	}
}

// TestResetTable confirms RopResetTable clears the column set, sort order, and
// restriction: after a filtered + sorted view, a reset and a fresh SetColumns
// return every row in store order.
func TestResetTable(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Charlie")
	seedInboxMessage(t, dir, "Alpha")
	seedInboxMessage(t, dir, "Bravo")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)
	mustDispatchOK(t, sess, buildSortTable(0, 0, 0, []sortOrderEntry{{mapi.PrSubject, sortDescend}}), []uint32{tableH}, ropSortTable)
	mustDispatchOK(t, sess, buildRestrict(0, propEq(mapi.PrSubject, "Charlie")), []uint32{tableH}, ropRestrict)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Charlie")

	mustDispatchOK(t, sess, buildResetTable(0), []uint32{tableH}, ropResetTable)
	// The reset cleared the columns too, so set them afresh; every row then returns
	// in store order with no filter or sort applied.
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)
	qr, _ = sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows = queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Charlie", "Alpha", "Bravo")
}

// buildFindRow builds a RopFindRow request (Flags, the restriction, Origin, and an
// empty Bookmark).
func buildFindRow(inIdx, flags, seekPos uint8, r *mapi.Restriction) []byte {
	var data []byte
	if r != nil {
		rd := ext.NewPush(ext.FlagUTF16)
		_ = rd.Restriction(*r)
		data = rd.Bytes()
	}
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropFindRow)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(flags)
	b.Uint16(uint16(len(data)))
	b.Raw(data)
	b.Uint8(seekPos)
	_ = b.BinShort(nil) // empty Bookmark
	return b.Bytes()
}

// findRowResponse parses a FindRow response: RowNoLongerVisible, HasRowData, and the
// row (when present).
func findRowResponse(t *testing.T, resp []byte, cols []mapi.PropTag) (found bool, row mapi.PropertyValues) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropFindRow {
		t.Fatalf("RopId = %#x, want FindRow", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("FindRow ec = %#x", ec)
	}
	mustU8(t, p, "rowNoLongerVisible")
	if hasRow := mustU8(t, p, "hasRowData"); hasRow == 0 {
		return false, nil
	}
	return true, decodeRow(t, p, cols)
}

// TestFindRow finds the first row matching a restriction from the beginning and
// confirms it lands the cursor on that row.
func TestFindRow(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Apple")
	seedInboxMessage(t, dir, "Banana")
	seedInboxMessage(t, dir, "Cherry")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	resp, _ := sess.Dispatch(buildFindRow(0, 0, bookmarkBeginning, propEq(mapi.PrSubject, "Banana")), []uint32{tableH})
	found, row := findRowResponse(t, resp, cols)
	if !found {
		t.Fatal("FindRow(Banana) found no row")
	}
	if subj, _ := row.Get(mapi.PrSubject); subj != "Banana" {
		t.Errorf("FindRow returned subject %v, want Banana", subj)
	}
	// The cursor now sits on the found row, so QueryRows pages from it.
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	assertSubjects(t, rows, "Banana", "Cherry")
}

// TestFindRowNoMatch confirms a search that matches nothing reports HasRowData=0
// rather than erroring.
func TestFindRowNoMatch(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Apple")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)
	cols := []mapi.PropTag{mapi.PrSubject}
	mustDispatchOK(t, sess, buildSetColumns(0, cols), []uint32{tableH}, ropSetColumns)

	resp, _ := sess.Dispatch(buildFindRow(0, 0, bookmarkBeginning, propEq(mapi.PrSubject, "Zucchini")), []uint32{tableH})
	if found, _ := findRowResponse(t, resp, cols); found {
		t.Error("FindRow(Zucchini) reported a row, want no match")
	}
}

// buildCreateBookmark builds a RopCreateBookmark request (no body).
func buildCreateBookmark(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCreateBookmark)
	b.Uint8(0)
	b.Uint8(inIdx)
	return b.Bytes()
}

// createBookmarkResponse parses a CreateBookmark response and returns the bookmark bytes.
func createBookmarkResponse(t *testing.T, resp []byte) []byte {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropCreateBookmark {
		t.Fatalf("RopId = %#x, want CreateBookmark", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CreateBookmark ec = %#x", ec)
	}
	bk, err := p.BinShort()
	if err != nil {
		t.Fatalf("CreateBookmark BinShort: %v", err)
	}
	return bk
}

// buildSeekRowBookmark builds a RopSeekRowBookmark request (bookmark, offset, WantMovedCount).
func buildSeekRowBookmark(inIdx uint8, bookmark []byte, offset int32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSeekRowBookmark)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.BinShort(bookmark)
	b.Uint32(uint32(offset))
	b.Uint8(0) // WantRowMovedCount
	return b.Bytes()
}

// seekRowBookmarkResponse parses a SeekRowBookmark response.
func seekRowBookmarkResponse(t *testing.T, resp []byte) (invisible uint8, hasSoughtLess uint8, sought int32) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSeekRowBookmark {
		t.Fatalf("RopId = %#x, want SeekRowBookmark", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SeekRowBookmark ec = %#x", ec)
	}
	invisible = mustU8(t, p, "rowInvisible")
	hasSoughtLess = mustU8(t, p, "hasSoughtLess")
	sought = int32(mustU32(t, p, "offsetSought"))
	return
}

// TestCreateBookmark creates a bookmark, seeks away, then seeks back via the bookmark.
func TestCreateBookmark(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"m0", "m1", "m2", "m3", "m4"} {
		seedInboxMessage(t, dir, s)
	}
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)

	// Seek to row 2 first
	mustDispatchOK(t, sess, buildSeekRow(0, bookmarkBeginning, 2), []uint32{tableH}, ropSeekRow)

	// Create bookmark at current cursor (row 2)
	resp, _ := sess.Dispatch(buildCreateBookmark(0), []uint32{tableH})
	bk := createBookmarkResponse(t, resp)

	// Seek forward to row 4
	mustDispatchOK(t, sess, buildSeekRow(0, bookmarkCurrent, 2), []uint32{tableH}, ropSeekRow)

	// Seek back to bookmark (should land at row 2)
	resp2, _ := sess.Dispatch(buildSeekRowBookmark(0, bk, 0), []uint32{tableH})
	invisible, hl, sought := seekRowBookmarkResponse(t, resp2)
	if invisible != 0 || hl != 0 || sought != 0 {
		t.Errorf("SeekRowBookmark(+0) = (invis %d, hl %d, sought %d), want (0,0,0)", invisible, hl, sought)
	}

	// QueryRows from the bookmarked position — should see m2 onwards
	mustDispatchOK(t, sess, buildSetColumns(0, []mapi.PropTag{mapi.PrSubject}), []uint32{tableH}, ropSetColumns)
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 3, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, []mapi.PropTag{mapi.PrSubject})
	assertSubjects(t, rows, "m2", "m3", "m4")
}

// TestSeekRowBookmarkNotFound returns ecNotFound for a non-existent bookmark.
func TestSeekRowBookmarkNotFound(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "m0")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)

	resp, _ := sess.Dispatch(buildSeekRowBookmark(0, []byte{0xFF, 0xFF}, 0), []uint32{tableH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "ropId") // ropSeekRowBookmark
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotFound {
		t.Errorf("SeekRowBookmark ec = %#x, want ecNotFound", ec)
	}
}

// buildSingleROP builds a minimal single-opcode ROP request for ROPs that have no
// body (or whose body is not consumed when returning ecNotSupported).
func buildSingleROP(ropID uint8, inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0)
	b.Uint8(inIdx)
	return b.Bytes()
}

// TestExpandCollapseUnsupported verifies expand/collapse ROPs return ecNotSupported
// since uncategorized (flat) tables have no category state.
func TestExpandCollapseUnsupported(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "m0")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openInboxContentsTable(t, sess)

	for _, tc := range []struct {
		name string
		req  []byte
	}{
		{"ExpandRow", buildSingleROP(ropExpandRow, 0)},
		{"CollapseRow", buildSingleROP(ropCollapseRow, 0)},
		{"SetCollapseState", buildSingleROP(ropSetCollapseState, 0)},
	} {
		resp, _ := sess.Dispatch(tc.req, []uint32{tableH})
		p := ext.NewPull(resp, ext.FlagUTF16)
		mustU8(t, p, "ropId")
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
			t.Errorf("%s: ec = %#x, want ecNotSupported", tc.name, ec)
		}
	}
}
