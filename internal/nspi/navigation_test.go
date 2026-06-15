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
	_ = p.TaggedPropVal(mapi.TaggedPropVal{Tag: mapi.PrDisplayName, Value: target})
	if table == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		_ = midArray(p, table)
	}
	if cols == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		_ = p.PropTagsLong(cols)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeSeek reads a SeekEntries response: result, the updated STAT, and the
// first decoded row (nil when no row set follows).
func decodeSeek(t *testing.T, resp []byte) (uint32, stat, mapi.PropertyValues) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	mustU32(t, p, "status")
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
	result, out, row := decodeSeek(t, s.SeekEntries(buildSeekEntries(st, "bob", nil, nil)))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if out.curRec != midBase+1 {
		t.Errorf("cur_rec = %#x, want %#x (bob)", out.curRec, midBase+1)
	}
	if out.numPos != 1 {
		t.Errorf("num_pos = %d, want 1", out.numPos)
	}
	if v, _ := row.Get(mapi.PrSmtpAddress); v != "bob@hermex.test" {
		t.Errorf("row SMTP = %v, want bob", v)
	}
}

// TestSeekEntriesPastEnd proves a target after every entry is not found.
func TestSeekEntriesPastEnd(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	st := stat{sortType: sortTypeDisplayName, codePage: 1252}
	result, _, _ := decodeSeek(t, s.SeekEntries(buildSeekEntries(st, "zzzz", nil, nil)))
	if result != ecNotFound {
		t.Errorf("result = %#x, want ecNotFound", result)
	}
}

// TestSeekEntriesRejectsNonDisplaySort proves a non-display-name sort is an error.
func TestSeekEntriesRejectsNonDisplaySort(t *testing.T) {
	s := testGAL("alice@hermex.test")
	st := stat{sortType: 0x99, codePage: 1252}
	result, _, _ := decodeSeek(t, s.SeekEntries(buildSeekEntries(st, "a", nil, nil)))
	if result != ecError {
		t.Errorf("result = %#x, want ecError", result)
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
	mustU32(t, p, "status")
	cmp = int32(mustU32(t, p, "cmp"))
	result = mustU32(t, p, "result")
	return cmp, result
}

// TestCompareMids proves the comparison reflects the entries' table order: the
// result is negative when the second MId precedes the first, positive when it
// follows, and zero when they are equal.
func TestCompareMids(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	alice, carol := midBase, midBase+2

	if cmp, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(carol, alice))); result != ecSuccess || cmp != -1 {
		t.Errorf("compare(carol, alice) = (cmp %d, result %#x), want (-1, ok) — alice precedes carol", cmp, result)
	}
	if cmp, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(alice, carol))); result != ecSuccess || cmp != 1 {
		t.Errorf("compare(alice, carol) = (cmp %d, result %#x), want (1, ok)", cmp, result)
	}
	if cmp, _ := decodeCompareMids(t, s.CompareMids(buildCompareMids(alice, alice))); cmp != 0 {
		t.Errorf("compare(alice, alice) cmp = %d, want 0", cmp)
	}
	if _, result := decodeCompareMids(t, s.CompareMids(buildCompareMids(alice, 0x9999))); result != ecError {
		t.Errorf("compare with an unknown MId result = %#x, want ecError", result)
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
		_ = midArray(p, inmids)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeResort reads a ResortRestriction response: result, the updated STAT, and
// the reordered MId list.
func decodeResort(t *testing.T, resp []byte) (uint32, stat, []uint32) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	mustU32(t, p, "status")
	result := mustU32(t, p, "result")
	mustU8(t, p, "STAT marker")
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
	result, out, mids := decodeResort(t, s.ResortRestriction(buildResort(st, in)))
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
	_, out, _ := decodeResort(t, s.ResortRestriction(buildResort(st, []uint32{midBase, midBase + 1})))
	if out.curRec != midBeginningOfTable {
		t.Errorf("cur_rec = %#x, want BEGINNING_OF_TABLE (current not in set)", out.curRec)
	}
	if out.numPos != 0 {
		t.Errorf("num_pos = %d, want 0", out.numPos)
	}
}
