package nspi

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildSeekEntries frames a SeekEntries request: reserved + a STAT + a
// display-name target + an optional explicit MId table + an optional column set.
func buildSeekEntries(st stat, target string, table []uint32, cols []mapi.PropTag) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)
	pushStat(p, st)
	p.Uint8(1) // hasTarget
	mustPush(p.TaggedPropVal(mapi.TaggedPropVal{Tag: mapi.PrDisplayName, Value: target}))
	if table == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		mustPush(midArray(p, table))
	}
	if cols == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		mustPush(p.PropTagsLong(cols))
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeSeek reads a SeekEntries response: result, the updated STAT, and the
// first decoded row (nil when no row set follows).
func decodeSeek(t *testing.T, resp []byte) (uint32, stat, mapi.PropertyValues) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	wantEq(t, "the status", mustU32(t, p, "status"), uint32(0))
	result := mustU32(t, p, "result")
	if m := mustU8(t, p, "STAT marker"); m != 0xFF {
		t.Fatalf("STAT marker = %#x, want 0xFF", m)
	}
	st, err := pullStat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mustU8(t, p, "rows marker") == 0 {
		return result, st, nil
	}
	cols, err := p.PropTagsLong()
	if err != nil {
		t.Fatal(err)
	}
	n := mustU32(t, p, "row count")
	if n == 0 {
		return result, st, nil
	}
	return result, st, decodeRow(t, p, cols)
}

// TestSeekEntries proves a display-name seek positions the cursor at the first
// entry at or after the target and returns that entry's row.
func TestSeekEntries(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	st := stat{sortType: sortTypeDisplayName, codePage: 1252}
	result, out, row := decodeSeek(t, s.SeekEntries(buildSeekEntries(st, "bob", nil, nil), testCaller))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if out.curRec != midBase+1 {
		t.Errorf("cur_rec = %#x, want %#x (bob)", out.curRec, midBase+1)
	}
	if out.numPos != 1 {
		t.Errorf("num_pos = %d, want 1", out.numPos)
	}
	if v, ok := row.Get(mapi.PrSmtpAddress); !ok || v != "bob@hermex.test" {
		t.Errorf("row SMTP = %v (ok=%v), want bob", v, ok)
	}
}

// TestSeekEntriesPastEnd proves a target after every entry is not found.
func TestSeekEntriesPastEnd(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	st := stat{sortType: sortTypeDisplayName, codePage: 1252}
	result, out, row := decodeSeek(t, s.SeekEntries(buildSeekEntries(st, "zzzz", nil, nil), testCaller))
	if result != ecNotFound {
		t.Errorf("result = %#x, want ecNotFound", result)
	}
	if row != nil || out.codePage != 1252 {
		t.Errorf("unexpected payload on not-found: row=%v codePage=%d", row, out.codePage)
	}
}

// TestSeekEntriesRejectsNonDisplaySort proves a non-display-name sort is an error.
func TestSeekEntriesRejectsNonDisplaySort(t *testing.T) {
	s := testGAL("alice@hermex.test")
	st := stat{sortType: 0x99, codePage: 1252}
	result, out, row := decodeSeek(t, s.SeekEntries(buildSeekEntries(st, "a", nil, nil), testCaller))
	if result != ecError {
		t.Errorf("result = %#x, want ecError", result)
	}
	if row != nil || out.sortType != 0x99 {
		t.Errorf("unexpected payload on error: row=%v sortType=%#x", row, out.sortType)
	}
}

// buildCompareMids frames a CompareMids request: reserved + a STAT + the two MIds.
func buildCompareMids(mid1, mid2 uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)
	pushStat(p, stat{codePage: 1252})
	p.Uint32(mid1)
	p.Uint32(mid2)
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeCompareMids reads a CompareMids response: status + cmp + result.
func decodeCompareMids(t *testing.T, resp []byte) (cmp int32, result uint32) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	wantEq(t, "the status", mustU32(t, p, "status"), uint32(0))
	cmp = int32(mustU32(t, p, "cmp"))
	result = mustU32(t, p, "result")
	return cmp, result
}

// TestCompareMids follows [MS-OXNSPI] 3.1.4.1.9: the result is less than 0 when
// MId1 comes before MId2 in the table, greater than 0 when it comes after, and 0
// when both name the same object.
func TestCompareMids(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	alice, carol := midBase, midBase+2

	if cmp, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(alice, carol), testCaller)); result != ecSuccess || cmp >= 0 {
		t.Errorf("compare(alice, carol) = (cmp %d, result %#x), want negative: MId1 alice comes before carol", cmp, result)
	}
	if cmp, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(carol, alice), testCaller)); result != ecSuccess || cmp <= 0 {
		t.Errorf("compare(carol, alice) = (cmp %d, result %#x), want positive: MId1 carol comes after alice", cmp, result)
	}
	if cmp, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(alice, alice), testCaller)); result != ecSuccess || cmp != 0 {
		t.Errorf("compare(alice, alice) = (cmp %d, result %#x), want (0, ecSuccess)", cmp, result)
	}
	if cmp, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(alice, 0x9999), testCaller)); result != ecError || cmp != 0 {
		t.Errorf("compare with an unknown MId = (cmp %d, result %#x), want (0, ecError)", cmp, result)
	}
}

// buildResort frames a ResortRestriction request: reserved + a STAT + the MId list.
func buildResort(st stat, inmids []uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)
	pushStat(p, st)
	if inmids == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		mustPush(midArray(p, inmids))
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeResort reads a ResortRestriction response: result, the updated STAT, and
// the reordered MId list.
func decodeResort(t *testing.T, resp []byte) (uint32, stat, []uint32) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	wantEq(t, "the status", mustU32(t, p, "status"), uint32(0))
	result := mustU32(t, p, "result")
	wantEq(t, "the STAT marker", mustU8(t, p, "STAT marker"), uint8(0xFF))
	st, err := pullStat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mustU8(t, p, "mids marker") == 0 {
		return result, st, nil
	}
	tags, err := p.PropTagsLong()
	if err != nil {
		t.Fatal(err)
	}
	mids := make([]uint32, len(tags))
	for i, tg := range tags {
		mids[i] = uint32(tg)
	}
	return result, st, mids
}

// TestResortRestriction proves a scrambled MId list is reordered into table
// (display-name) order, an unknown MId is dropped, and total_rec is updated.
func TestResortRestriction(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	in := []uint32{midBase + 2, 0x9999, midBase, midBase + 1} // scrambled + an unknown
	st := stat{codePage: 1252, curRec: midBase + 1}           // cur_rec (bob) is in the set
	result, out, mids := decodeResort(t, s.ResortRestriction(buildResort(st, in), testCaller))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	want := []uint32{midBase, midBase + 1, midBase + 2}
	if len(mids) != len(want) {
		t.Fatalf("reordered %d MIds, want %d (unknown dropped)", len(mids), len(want))
	}
	for i, w := range want {
		if mids[i] != w {
			t.Errorf("mids[%d] = %#x, want %#x (display order)", i, mids[i], w)
		}
	}
	if out.totalRec != 3 {
		t.Errorf("total_rec = %d, want 3", out.totalRec)
	}
	if out.curRec != midBase+1 {
		t.Errorf("cur_rec = %#x, want %#x (in the set, preserved)", out.curRec, midBase+1)
	}
}

// TestResortRestrictionCursorReset proves the cursor resets to the table start
// when the STAT's current record is not in the reordered set.
func TestResortRestrictionCursorReset(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	st := stat{codePage: 1252, curRec: 0x9999} // not in the set
	result, out, mids := decodeResort(t, s.ResortRestriction(buildResort(st, []uint32{midBase, midBase + 1}), testCaller))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if len(mids) != 2 {
		t.Errorf("mids = %d, want 2", len(mids))
	}
	if out.curRec != midBeginningOfTable {
		t.Errorf("cur_rec = %#x, want BEGINNING_OF_TABLE (current not in set)", out.curRec)
	}
	if out.numPos != 0 {
		t.Errorf("num_pos = %d, want 0", out.numPos)
	}
}
