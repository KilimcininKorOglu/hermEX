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

	sess := NewSession(dir)
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
	sess := NewSession(dir)
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
