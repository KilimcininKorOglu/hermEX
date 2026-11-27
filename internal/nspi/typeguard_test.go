package nspi

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// TestSeekEntriesRefusesANonStringTarget proves a SeekEntries target that names
// the display-name property ID but carries a non-string type ([MS-OXNSPI]
// 3.1.4.1.10 seeks on a display name) answers ecError, rather than seeking on an
// empty name and returning the first entry of the table.
func TestSeekEntriesRefusesANonStringTarget(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test"},
		{DisplayName: "bob@hermex.test", Address: "bob@hermex.test"},
	}, testGUID)
	longName := mapi.PropTag(uint32(mapi.PrDisplayName)&0xFFFF0000 | uint32(mapi.PtLong))
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)
	// The same STAT the refusal is checked against below.
	pushStat(p, stat{sortType: sortTypeDisplayName, codePage: 1252})
	p.Uint8(1) // hasTarget
	mustPush(p.TaggedPropVal(mapi.TaggedPropVal{Tag: longName, Value: int32(5)}))
	p.Uint8(0)  // no table
	p.Uint8(0)  // no columns
	p.Uint32(0) // cb_auxin
	st := stat{sortType: sortTypeDisplayName, codePage: 1252}
	result, out, row := decodeSeek(t, s.SeekEntries(p.Bytes(), testCaller))
	if result != ecError || len(row) != 0 {
		t.Errorf("SeekEntries on a PT_LONG display name = (result %#x, row %v), want (ecError, no row)", result, row)
	}
	if out.curRec != st.curRec {
		t.Errorf("refused SeekEntries moved the cursor to %#x, want %#x", out.curRec, st.curRec)
	}
}

// TestMatchNodeRefusesAnAndWithoutChildren proves an AND restriction whose value
// is not a child list matches no entry. An empty AND is true for every entry, so
// reading a malformed node as one would turn a filter into "everyone".
func TestMatchNodeRefusesAnAndWithoutChildren(t *testing.T) {
	u := galUser{display: "alice@hermex.test", smtp: "alice@hermex.test"}
	for _, typ := range []mapi.RestrictionType{mapi.ResAnd, mapi.ResOr} {
		res := mapi.Restriction{Type: typ, Value: "not a child list"}
		if matchNode(u, &res) {
			t.Errorf("restriction type %#x with a non-list value matched", typ)
		}
	}
}

// TestPushPropValContentRefusesAMismatchedType proves the NDR content pass of a
// pointer-typed property refuses a value of another Go type instead of writing an
// empty string or an empty blob under the property's tag.
func TestPushPropValContentRefusesAMismatchedType(t *testing.T) {
	string8Name := mapi.PropTag(uint32(mapi.PrDisplayName)&0xFFFF0000 | uint32(mapi.PtString8))
	for _, tag := range []mapi.PropTag{mapi.PrDisplayName, string8Name, mapi.PrEntryID, mapi.PrEmsAbX509Cert} {
		p := ndr.NewPush()
		if err := pushPropValContentNDR(p, tag, int32(1)); err == nil {
			t.Errorf("tag %#x with an int32 value encoded %d bytes, want a type error", uint32(tag), len(p.Bytes()))
		}
	}
}
