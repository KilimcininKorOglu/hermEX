package nspi

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// TestNDRStatRoundTrip pushes a STAT with distinct field values (including a
// negative delta) and pulls it back, and pins the on-wire length at 36 bytes
// (nine 4-byte fields, no trailing pad under NDR32).
func TestNDRStatRoundTrip(t *testing.T) {
	in := stat{sortType: 1, containerID: 2, curRec: 3, delta: -7, numPos: 5,
		totalRec: 6, codePage: 1252, tplLocale: 0x0409, sortLocale: 0x040A}
	p := ndr.NewPush()
	pushStatNDR(p, in)
	if len(p.Bytes()) != 36 {
		t.Fatalf("STAT wire length = %d, want 36", len(p.Bytes()))
	}
	out, err := pullStatNDR(ndr.NewPull(p.Bytes()))
	if err != nil {
		t.Fatalf("pull stat: %v", err)
	}
	if out != in {
		t.Errorf("STAT round-trip = %+v, want %+v", out, in)
	}
}

// TestNDRUnicodeValueVector is the independent length-math vector for a
// PtypString value content: the conformant count is in UTF-16 CODE UNITS
// (terminator included), while the payload is twice that in bytes. "AB" →
// max_count=3, offset=0, actual_count=3, then 41 00 42 00 00 00. A symmetric
// push/pull bug that counted bytes would pass round-trip but fail this.
func TestNDRUnicodeValueVector(t *testing.T) {
	p := ndr.NewPush()
	if err := pushPropValContentNDR(p, mapi.PrDisplayName, "AB"); err != nil {
		t.Fatalf("push unicode content: %v", err)
	}
	want := []byte{
		0x03, 0x00, 0x00, 0x00, // max_count = 3 code units
		0x00, 0x00, 0x00, 0x00, // offset = 0
		0x03, 0x00, 0x00, 0x00, // actual_count = 3
		0x41, 0x00, 0x42, 0x00, 0x00, 0x00, // "AB\0" UTF-16LE
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("unicode value content = % x, want % x", p.Bytes(), want)
	}
}

// TestNDRProptagArrayVector is the independent length-math vector for the
// proptag/MID array: the conformant max_count is deliberately N+1 while cValues
// and the varying length are N. An off-by-one that is symmetric in push/pull
// survives round-trip but fails this byte assert.
func TestNDRProptagArrayVector(t *testing.T) {
	p := ndr.NewPush()
	pushU32ArrayNDR(p, []uint32{0x12345678, 0x9ABCDEF0})
	want := []byte{
		0x03, 0x00, 0x00, 0x00, // max_count = N+1 = 3
		0x02, 0x00, 0x00, 0x00, // cValues = 2
		0x00, 0x00, 0x00, 0x00, // offset = 0
		0x02, 0x00, 0x00, 0x00, // length = 2
		0x78, 0x56, 0x34, 0x12, // 0x12345678
		0xF0, 0xDE, 0xBC, 0x9A, // 0x9ABCDEF0
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("proptag array = % x, want % x", p.Bytes(), want)
	}
	got, err := pullU32ArrayNDR(ndr.NewPull(p.Bytes()))
	if err != nil {
		t.Fatalf("pull proptag array: %v", err)
	}
	if len(got) != 2 || got[0] != 0x12345678 || got[1] != 0x9ABCDEF0 {
		t.Errorf("proptag array round-trip = %#x, want [12345678 9abcdef0]", got)
	}
}

// TestNDRInlineMIDArray pins the QueryRows-IN inline MID array shape: a count, a
// referent, then a conformant max_count that EQUALS the count (no N+1, no
// offset/length). This is the one array that must NOT go through the proptag
// helper; the pin keeps 5c from routing it there.
func TestNDRInlineMIDArray(t *testing.T) {
	buf := []byte{
		0x02, 0x00, 0x00, 0x00, // count = 2
		0x00, 0x00, 0x02, 0x00, // referent (non-null)
		0x02, 0x00, 0x00, 0x00, // max_count = 2 (== count)
		0x64, 0x00, 0x00, 0x00, // 100
		0xC8, 0x00, 0x00, 0x00, // 200
	}
	got, err := pullInlineMIDArrayNDR(ndr.NewPull(buf))
	if err != nil {
		t.Fatalf("pull inline MID array: %v", err)
	}
	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Errorf("inline MID array = %v, want [100 200]", got)
	}
	// A null referent yields no array (the client sent no explicit list).
	null := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if got, err := pullInlineMIDArrayNDR(ndr.NewPull(null)); err != nil || got != nil {
		t.Errorf("null inline MID array = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestNDRPropValRoundTrip pushes each property type the GAL emits (header then
// content, contiguous as a standalone value is laid out) and pulls it back.
func TestNDRPropValRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		tag   mapi.PropTag
		value any
	}{
		{"long", mapi.PrObjectType, int32(6)},
		{"unicode", mapi.PrDisplayName, "alice örnek"},
		{"string8", mapi.PrAddrType, "SMTP"}, // pull supports String8 for restriction values
		{"binary", mapi.PrEntryID, []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"boolean", mapi.PrEmsAbIsMaster, true},
		{"error", errorTag(mapi.PrObjectType), uint32(ecNotFound)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tag := c.tag
			if c.name == "string8" {
				tag = mapi.PropTag(uint32(c.tag)&0xFFFF0000 | uint32(mapi.PtString8))
			}
			p := ndr.NewPush()
			if err := pushPropValHeaderNDR(p, tag, c.value); err != nil {
				t.Fatalf("push header: %v", err)
			}
			if err := pushPropValContentNDR(p, tag, c.value); err != nil {
				t.Fatalf("push content: %v", err)
			}
			tv, err := pullPropValNDR(ndr.NewPull(p.Bytes()))
			if err != nil {
				t.Fatalf("pull value: %v", err)
			}
			if tv.Tag != tag {
				t.Errorf("tag = %#x, want %#x", uint32(tv.Tag), uint32(tag))
			}
			if b, ok := c.value.([]byte); ok {
				if got, _ := tv.Value.([]byte); !bytes.Equal(got, b) {
					t.Errorf("binary value = % x, want % x", got, b)
				}
			} else if tv.Value != c.value {
				t.Errorf("value = %#v, want %#v", tv.Value, c.value)
			}
		})
	}
}

// TestNDRRowSet pushes a one-row, one-column PROPROW_SET and walks it back,
// asserting the row/value framing (counts, present referents) and decoding the
// string value. Referent ids are message-global and ignored by the client, so
// they are checked for presence (non-zero), not pinned to a value.
func TestNDRRowSet(t *testing.T) {
	cols := []mapi.PropTag{mapi.PrDisplayName}
	rows := []mapi.PropertyValues{{{Tag: mapi.PrDisplayName, Value: "alice"}}}
	p := ndr.NewPush()
	if err := pushRowSetNDR(p, cols, rows); err != nil {
		t.Fatalf("push rowset: %v", err)
	}
	pl := ndr.NewPull(p.Bytes())
	mustU32NDR(t, pl, "crows max_count", 1)
	mustU32NDR(t, pl, "crows actual", 1)
	mustU32NDR(t, pl, "row reserved", 0)
	mustU32NDR(t, pl, "row cValues", 1)
	if ref, _ := pl.Uint32(); ref == 0 {
		t.Error("row values referent is null, want non-zero")
	}
	mustU32NDR(t, pl, "value-array max_count", 1)
	// Value header: proptag, reserved, ptype.
	mustU32NDR(t, pl, "value proptag", uint32(mapi.PrDisplayName))
	mustU32NDR(t, pl, "value reserved", 0)
	mustU32NDR(t, pl, "value ptype", uint32(mapi.PtUnicode))
	if ref, _ := pl.Uint32(); ref == 0 {
		t.Error("string value referent is null, want non-zero")
	}
	// Value content: the conformant string.
	mustU32NDR(t, pl, "string max_count", 6) // "alice\0" = 6 code units
	mustU32NDR(t, pl, "string offset", 0)
	mustU32NDR(t, pl, "string actual", 6)
	raw, _ := pl.Raw(12)
	if got := decodeUTF16LE(raw); got != "alice" {
		t.Errorf("decoded string = %q, want %q", got, "alice")
	}
}

// TestNDRRowSetProjection proves a column the row lacks is emitted as a
// PT_ERROR value, the same projection the MAPI/HTTP encoder uses.
func TestNDRRowSetProjection(t *testing.T) {
	cols := []mapi.PropTag{mapi.PrDisplayName, mapi.PrObjectType}
	rows := []mapi.PropertyValues{{{Tag: mapi.PrDisplayName, Value: "bob"}}} // PrObjectType absent
	p := ndr.NewPush()
	if err := pushRowSetNDR(p, cols, rows); err != nil {
		t.Fatalf("push rowset: %v", err)
	}
	pl := ndr.NewPull(p.Bytes())
	mustU32NDR(t, pl, "crows max_count", 1)
	mustU32NDR(t, pl, "crows actual", 1)
	mustU32NDR(t, pl, "row reserved", 0)
	mustU32NDR(t, pl, "row cValues", 2) // both columns present in the row, the 2nd an error
	pl.Uint32()                         // referent
	mustU32NDR(t, pl, "value-array max_count", 2)
	// First value: PrDisplayName (unicode, present).
	mustU32NDR(t, pl, "v0 proptag", uint32(mapi.PrDisplayName))
	pl.Uint32() // reserved
	mustU32NDR(t, pl, "v0 ptype", uint32(mapi.PtUnicode))
	pl.Uint32() // string referent
	// Second value: the absent column rewritten to PT_ERROR.
	if tag, _ := pl.Uint32(); mapi.PropTag(tag).Type() != mapi.PtError {
		t.Errorf("absent column type = %#x, want PtError", mapi.PropTag(tag).Type())
	}
	pl.Uint32() // reserved
	mustU32NDR(t, pl, "v1 ptype", uint32(mapi.PtError))
	mustU32NDR(t, pl, "v1 scode", uint32(ecNotFound))
}

// TestNDRPropertyRow pushes a single PROPERTY_ROW (the GetProps OUT shape, not a
// rowset) and walks back its header and one projected value.
func TestNDRPropertyRow(t *testing.T) {
	cols := []mapi.PropTag{mapi.PrObjectType}
	row := mapi.PropertyValues{{Tag: mapi.PrObjectType, Value: int32(6)}}
	p := ndr.NewPush()
	if err := pushPropertyRowNDR(p, cols, row); err != nil {
		t.Fatalf("push property row: %v", err)
	}
	pl := ndr.NewPull(p.Bytes())
	mustU32NDR(t, pl, "reserved", 0)
	mustU32NDR(t, pl, "cValues", 1)
	if ref, _ := pl.Uint32(); ref == 0 {
		t.Error("values referent is null, want non-zero")
	}
	mustU32NDR(t, pl, "value-array max_count", 1)
	mustU32NDR(t, pl, "proptag", uint32(mapi.PrObjectType))
	mustU32NDR(t, pl, "reserved", 0)
	mustU32NDR(t, pl, "ptype", uint32(mapi.PtLong))
	mustU32NDR(t, pl, "long value", 6)
}

// TestNDRPushProptags proves the PropTag convenience wrapper emits the same
// N+1-conformant array as the u32 helper.
func TestNDRPushProptags(t *testing.T) {
	tags := []mapi.PropTag{mapi.PrEntryID, mapi.PrDisplayName}
	p := ndr.NewPush()
	pushProptagsNDR(p, tags)
	got, err := pullU32ArrayNDR(ndr.NewPull(p.Bytes()))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 2 || got[0] != uint32(mapi.PrEntryID) || got[1] != uint32(mapi.PrDisplayName) {
		t.Errorf("proptags round-trip = %#x, want [%#x %#x]", got, uint32(mapi.PrEntryID), uint32(mapi.PrDisplayName))
	}
}

// TestNDRStringsArray pulls a hand-built conformant strings array in both the
// wide (ResolveNamesW) and narrow (DNToMId) forms.
func TestNDRStringsArray(t *testing.T) {
	// Narrow: ["hi"] → count 1, array referent, max_count 1, one element
	// referent, then max/off/act=3 + "hi\0".
	narrow := []byte{
		0x01, 0x00, 0x00, 0x00, // count
		0x00, 0x00, 0x02, 0x00, // array referent
		0x01, 0x00, 0x00, 0x00, // max_count
		0x04, 0x00, 0x02, 0x00, // element referent
		0x03, 0x00, 0x00, 0x00, // string max_count
		0x00, 0x00, 0x00, 0x00, // offset
		0x03, 0x00, 0x00, 0x00, // actual
		'h', 'i', 0x00, // "hi\0"
	}
	got, err := pullStringsArrayNDR(ndr.NewPull(narrow), false)
	if err != nil || len(got) != 1 || got[0] != "hi" {
		t.Fatalf("narrow strings array = (%v, %v), want ([hi], nil)", got, err)
	}

	// Wide: ["AB"] UTF-16, string actual=3 code units, 6 payload bytes.
	wide := []byte{
		0x01, 0x00, 0x00, 0x00, // count
		0x00, 0x00, 0x02, 0x00, // array referent
		0x01, 0x00, 0x00, 0x00, // max_count
		0x04, 0x00, 0x02, 0x00, // element referent
		0x03, 0x00, 0x00, 0x00, // string max_count (code units)
		0x00, 0x00, 0x00, 0x00, // offset
		0x03, 0x00, 0x00, 0x00, // actual
		0x41, 0x00, 0x42, 0x00, 0x00, 0x00, // "AB\0"
	}
	got, err = pullStringsArrayNDR(ndr.NewPull(wide), true)
	if err != nil || len(got) != 1 || got[0] != "AB" {
		t.Fatalf("wide strings array = (%v, %v), want ([AB], nil)", got, err)
	}
}

// TestNDRCtxHandle round-trips a 20-byte CONTEXT_HANDLE.
func TestNDRCtxHandle(t *testing.T) {
	guid := mapi.GUID{Data1: 0x11223344, Data2: 0x5566, Data3: 0x7788, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}
	p := ndr.NewPush()
	pushCtxHandleNDR(p, 0, guid)
	if len(p.Bytes()) != 20 {
		t.Fatalf("ctx handle length = %d, want 20", len(p.Bytes()))
	}
	ht, got, err := pullCtxHandleNDR(ndr.NewPull(p.Bytes()))
	if err != nil || ht != 0 || got != guid {
		t.Errorf("ctx handle round-trip = (%d, %v, %v), want (0, %v, nil)", ht, got, err, guid)
	}
}

// mustU32NDR reads a u32 and fails if it does not equal want.
func mustU32NDR(t *testing.T, p *ndr.Pull, label string, want uint32) {
	t.Helper()
	got, err := p.Uint32()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if got != want {
		t.Fatalf("%s = %d (%#x), want %d", label, got, got, want)
	}
}
