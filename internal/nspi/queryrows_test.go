package nspi

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestPushPropertyRowForms byte-checks the proprow framing, the advisor's
// fragile keystone. A NONE row (flag 0x00, bare values) when every column is
// present; a FLAGGED row (flag 0x01, per-column available/error markers) when
// one is missing. The value bytes ride on internal/ext's reference-gated ABK
// encoding; this pins the framing layered on top.
func TestPushPropertyRowForms(t *testing.T) {
	bag := mapi.PropertyValues{
		{Tag: mapi.PrDisplayName, Value: "Alice"},
		{Tag: mapi.PrObjectType, Value: int32(6)},
	}

	// NONE form: every column present.
	none := []mapi.PropTag{mapi.PrDisplayName, mapi.PrObjectType}
	p := ext.NewPush(abkFlags)
	mustNoErr(t, "push the NONE row", pushPropertyRow(p, none, bag))
	b := p.Bytes()
	wantEq(t, "the NONE row flag", b[0], uint8(propRowNone))
	pull := ext.NewPull(b, abkFlags)
	mustU8(t, pull, "flag")
	v, _ := pull.PropValue(mapi.PtUnicode)
	wantEq(t, "the NONE row display name", v, any("Alice"))
	v, _ = pull.PropValue(mapi.PtLong)
	wantEq(t, "the NONE row object type", v, any(int32(6)))

	// FLAGGED form: PrSmtpAddress is absent -> a PT_ERROR (ecNotFound) marker.
	flagged := []mapi.PropTag{mapi.PrDisplayName, mapi.PrSmtpAddress}
	p2 := ext.NewPush(abkFlags)
	mustNoErr(t, "push the FLAGGED row", pushPropertyRow(p2, flagged, bag))
	b2 := p2.Bytes()
	wantEq(t, "the FLAGGED row flag", b2[0], uint8(propRowFlagged))
	pull2 := ext.NewPull(b2, abkFlags)
	mustU8(t, pull2, "flag")
	present, _ := pull2.FlaggedPropVal(mapi.PtUnicode)
	wantEq(t, "the present column's flag", present.Flag, mapi.FlaggedAvailable)
	wantEq(t, "the present column's value", present.Value, any("Alice"))
	fpv, _ := pull2.FlaggedPropVal(mapi.PtUnicode)
	wantEq(t, "the absent column's flag", fpv.Flag, mapi.FlaggedError)
	if fpv.Value != ecNotFound {
		t.Errorf("absent column ec = %v, want ecNotFound", fpv.Value)
	}
}

// buildQueryRows frames a QueryRows request.
func buildQueryRows(st stat, explicit []uint32, count uint32, columns []mapi.PropTag) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // flags
	p.Uint8(1)  // hasStat
	pushStat(p, st)
	tags := make([]mapi.PropTag, len(explicit))
	for i, m := range explicit {
		tags[i] = mapi.PropTag(m)
	}
	_ = p.PropTagsLong(tags) // explicit MId list (empty when nil)
	p.Uint32(count)
	if columns == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		_ = p.PropTagsLong(columns)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeRow reads one PROPERTY_ROW against cols, returning the available values.
func decodeRow(t *testing.T, p *ext.Pull, cols []mapi.PropTag) mapi.PropertyValues {
	t.Helper()
	flag := mustU8(t, p, "row flag")
	var row mapi.PropertyValues
	for _, c := range cols {
		if flag == propRowNone {
			v, err := p.PropValue(c.Type())
			if err != nil {
				t.Fatalf("decode NONE col %#x: %v", uint32(c), err)
			}
			row = append(row, mapi.TaggedPropVal{Tag: c, Value: v})
		} else {
			fpv, err := p.FlaggedPropVal(c.Type())
			if err != nil {
				t.Fatalf("decode FLAGGED col %#x: %v", uint32(c), err)
			}
			if fpv.Flag == mapi.FlaggedAvailable {
				row = append(row, mapi.TaggedPropVal{Tag: c, Value: fpv.Value})
			}
		}
	}
	return row
}

// TestQueryRowsWalk proves a from-the-start cursor walk returns every GAL row,
// advances the STAT to END_OF_TABLE, and the rows decode to the seeded users.
func TestQueryRowsWalk(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	body := buildQueryRows(stat{curRec: midBeginningOfTable, codePage: 1252}, nil, 10, nil)
	p := ext.NewPull(s.QueryRows(body, testCaller), abkFlags)

	if status := mustU32(t, p, "status"); status != 0 {
		t.Fatalf("status = %#x, want 0", status)
	}
	if result := mustU32(t, p, "result"); result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	mustU8(t, p, "STAT marker")
	outStat, err := pullStat(p)
	mustNoErr(t, "decode the STAT", err)
	wantEq(t, "cur_rec (all rows fit the count, so END_OF_TABLE)", outStat.curRec, uint32(midEndOfTable))
	wantEq(t, "the STAT num_pos", outStat.numPos, uint32(3))
	wantEq(t, "the STAT total_rec", outStat.totalRec, uint32(3))

	mustU8(t, p, "rows marker")
	cols, err := p.PropTagsLong()
	mustNoErr(t, "decode the columns", err)
	wantEq(t, "the column count (the default set)", len(cols), len(defaultColumns))
	wantEq(t, "the row count", mustU32(t, p, "row count"), uint32(3))

	row0 := decodeRow(t, p, cols)
	v, _ := row0.Get(mapi.PrSmtpAddress)
	wantEq(t, "row[0] SMTP (address-sorted first)", v, any("alice@hermex.test"))
	v, _ = row0.Get(mapi.PrAddrType)
	wantEq(t, "row[0] ADDRTYPE", v, any("SMTP"))
}

// TestQueryRowsExplicit proves the explicit-MId mode returns one row per id (an
// all-error row for an unknown id) and leaves the cursor untouched.
func TestQueryRowsExplicit(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	body := buildQueryRows(stat{codePage: 1252}, []uint32{midBase + 1, 0x9999}, 10, nil)
	p := ext.NewPull(s.QueryRows(body, testCaller), abkFlags)
	mustU32(t, p, "status")
	if result := mustU32(t, p, "result"); result != ecSuccess {
		t.Fatalf("result = %#x", result)
	}
	mustU8(t, p, "STAT marker")
	_, _ = pullStat(p)
	mustU8(t, p, "rows marker")
	cols, _ := p.PropTagsLong()
	if n := mustU32(t, p, "row count"); n != 2 {
		t.Fatalf("row count = %d, want 2 (one per explicit id)", n)
	}
	known := decodeRow(t, p, cols)
	if v, _ := known.Get(mapi.PrSmtpAddress); v != "bob@hermex.test" {
		t.Errorf("explicit MId 0x11 SMTP = %v, want bob", v)
	}
	unknown := decodeRow(t, p, cols)
	if _, ok := unknown.Get(mapi.PrSmtpAddress); ok {
		t.Error("unknown MId returned a value, want an all-error row")
	}
}

// TestUpdateStatAdvance proves UpdateStat repositions the cursor by the delta
// and reports the applied row delta when requested.
func TestUpdateStatAdvance(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)  // hasStat
	pushStat(p, stat{curRec: midBeginningOfTable, delta: 2, codePage: 1252})
	p.Uint8(1)  // delta_requested
	p.Uint32(0) // cb_auxin

	pr := ext.NewPull(s.UpdateStat(p.Bytes(), testCaller), abkFlags)
	mustU32(t, pr, "status")
	if result := mustU32(t, pr, "result"); result != ecSuccess {
		t.Fatalf("result = %#x", result)
	}
	mustU8(t, pr, "STAT marker")
	st, _ := pullStat(pr)
	if st.numPos != 2 {
		t.Errorf("num_pos = %d, want 2", st.numPos)
	}
	if st.curRec != midBase+2 {
		t.Errorf("cur_rec = %#x, want %#x (carol)", st.curRec, midBase+2)
	}
	if m := mustU8(t, pr, "delta marker"); m != 0xFF {
		t.Fatalf("delta marker = %#x, want 0xFF (requested)", m)
	}
	if d := mustU32(t, pr, "delta"); int32(d) != 2 {
		t.Errorf("applied delta = %d, want 2", int32(d))
	}
}

// TestQueryColumns proves QueryColumns reports the default address-book column
// set.
func TestQueryColumns(t *testing.T) {
	s := testGAL("alice@hermex.test")
	body := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0} // reserved + flags + cb_auxin
	p := ext.NewPull(s.QueryColumns(body), abkFlags)
	mustU32(t, p, "status")
	if result := mustU32(t, p, "result"); result != ecSuccess {
		t.Fatalf("result = %#x", result)
	}
	if m := mustU8(t, p, "columns marker"); m != 0xFF {
		t.Fatalf("columns marker = %#x, want 0xFF", m)
	}
	cols, _ := p.PropTagsLong()
	if len(cols) != len(defaultColumns) {
		t.Errorf("columns = %d, want %d", len(cols), len(defaultColumns))
	}
}

// TestQueryRowsZeroCount proves a count of zero is rejected.
func TestQueryRowsZeroCount(t *testing.T) {
	s := testGAL("alice@hermex.test")
	body := buildQueryRows(stat{codePage: 1252}, nil, 0, nil)
	p := ext.NewPull(s.QueryRows(body, testCaller), abkFlags)
	mustU32(t, p, "status")
	if result := mustU32(t, p, "result"); result != ecInvalidParam {
		t.Errorf("count=0 result = %#x, want ecInvalidParam", result)
	}
}
