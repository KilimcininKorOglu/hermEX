package ext

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

// Each value's Go type must match what mapi.TaggedPropVal documents; the
// round trip proves the type switch picks the right wire codec both ways.
func TestTaggedPropValRoundTrip(t *testing.T) {
	guid := mapi.GUID{Data1: 0x00062002, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	cases := []mapi.TaggedPropVal{
		{Tag: mapi.MakeTag(0x0001, mapi.PtShort), Value: int16(-3)},
		{Tag: mapi.MakeTag(0x0002, mapi.PtLong), Value: int32(-100000)},
		{Tag: mapi.MakeTag(0x0003, mapi.PtError), Value: uint32(0x80040107)},
		{Tag: mapi.MakeTag(0x0004, mapi.PtFloat), Value: float32(1.5)},
		{Tag: mapi.MakeTag(0x0005, mapi.PtDouble), Value: float64(3.14159)},
		{Tag: mapi.MakeTag(0x0006, mapi.PtI8), Value: int64(1 << 40)},
		{Tag: mapi.MakeTag(0x0007, mapi.PtSysTime), Value: uint64(0x01D9A1B2C3D4E5F6)},
		{Tag: mapi.MakeTag(0x0008, mapi.PtBoolean), Value: true},
		{Tag: mapi.MakeTag(0x0009, mapi.PtString8), Value: "ascii"},
		{Tag: mapi.MakeTag(0x000A, mapi.PtUnicode), Value: "ünïçödé"},
		{Tag: mapi.MakeTag(0x000B, mapi.PtCLSID), Value: guid},
		{Tag: mapi.MakeTag(0x000C, mapi.PtBinary), Value: []byte{1, 2, 3}},
		{Tag: mapi.MakeTag(0x000D, mapi.PtMvLong), Value: []int32{1, -2, 3}},
		{Tag: mapi.MakeTag(0x000E, mapi.PtMvI8), Value: []int64{9, 10}},
		{Tag: mapi.MakeTag(0x000F, mapi.PtMvUnicode), Value: []string{"a", "bb"}},
		{Tag: mapi.MakeTag(0x0010, mapi.PtMvBinary), Value: [][]byte{{0xAA}, {0xBB, 0xCC}}},
		{Tag: mapi.MakeTag(0x0011, mapi.PtMvCLSID), Value: []mapi.GUID{guid}},
	}
	// Unicode and multivalue strings must use UTF-16 to exercise that path.
	const flags = FlagUTF16
	for _, c := range cases {
		p := NewPush(flags)
		if err := p.TaggedPropVal(c); err != nil {
			t.Fatalf("%s push: %v", c.Tag, err)
		}
		got, err := NewPull(p.Bytes(), flags).TaggedPropVal()
		if err != nil {
			t.Fatalf("%s pull: %v", c.Tag, err)
		}
		if got.Tag != c.Tag || !reflect.DeepEqual(got.Value, c.Value) {
			t.Errorf("%s round trip = %#v, want %#v", c.Tag, got.Value, c.Value)
		}
	}
}

func TestTaggedPropValKnownVector(t *testing.T) {
	// PR_SUBJECT_W = 0x0037001F with "Hi" under UTF-16: the 32-bit tag (LE)
	// then UTF-16LE "Hi" with the 00 00 terminator.
	tp := mapi.TaggedPropVal{Tag: 0x0037001F, Value: "Hi"}
	p := NewPush(FlagUTF16)
	if err := p.TaggedPropVal(tp); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x1F, 0x00, 0x37, 0x00, 0x48, 0x00, 0x69, 0x00, 0x00, 0x00}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("PR_SUBJECT_W = % x, want % x", p.Bytes(), want)
	}
}

func TestPropertyValuesRoundTrip(t *testing.T) {
	pv := mapi.PropertyValues{
		{Tag: mapi.MakeTag(0x0037, mapi.PtUnicode), Value: "subject"},
		{Tag: mapi.MakeTag(0x0E08, mapi.PtI8), Value: int64(4096)},
		{Tag: mapi.MakeTag(0x0FFF, mapi.PtBinary), Value: []byte{0xDE, 0xAD}},
	}
	p := NewPush(FlagUTF16)
	if err := p.PropertyValues(pv); err != nil {
		t.Fatal(err)
	}
	// TPROPVAL_ARRAY count is a uint16.
	if p.Bytes()[0] != 0x03 || p.Bytes()[1] != 0x00 {
		t.Errorf("count prefix = % x, want 03 00", p.Bytes()[:2])
	}
	got, err := NewPull(p.Bytes(), FlagUTF16).PropertyValues()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, pv) {
		t.Errorf("PropertyValues round trip = %#v, want %#v", got, pv)
	}
}

func TestPropTagsRoundTripAndVector(t *testing.T) {
	tags := []mapi.PropTag{0x0037001F, 0x0E1D001F}
	p := NewPush(0)
	if err := p.PropTags(tags); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x02, 0x00, 0x1F, 0x00, 0x37, 0x00, 0x1F, 0x00, 0x1D, 0x0E}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("PropTags = % x, want % x", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).PropTags()
	if err != nil || !reflect.DeepEqual(got, tags) {
		t.Errorf("PropTags round trip = %v (%v)", got, err)
	}
}

func TestPropValueWrongGoType(t *testing.T) {
	// PtLong expects int32; a string must be rejected, not silently mis-encoded.
	err := NewPush(0).PropValue(mapi.PtLong, "not an int32")
	if !errors.Is(err, ErrFormat) {
		t.Errorf("wrong-type err = %v, want ErrFormat", err)
	}
}

func TestPropValueUnsupportedType(t *testing.T) {
	// PtActions is deferred; it must fail loudly rather than emit wrong bytes.
	err := NewPush(0).PropValue(mapi.PtActions, []byte{1})
	if !errors.Is(err, ErrFormat) {
		t.Errorf("unsupported-type err = %v, want ErrFormat", err)
	}
}
