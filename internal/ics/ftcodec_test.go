package ics

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

func tag(id uint16, typ mapi.PropType) mapi.PropTag {
	return mapi.PropTag(uint32(id)<<16 | uint32(typ))
}

// encodeOne encodes a property and returns the concatenated header+body (one
// element as it lands on the wire).
func encodeOne(t *testing.T, p StreamProp) []byte {
	t.Helper()
	h, b, err := encodeProp(p)
	if err != nil {
		t.Fatalf("encodeProp(%s): %v", p.Tag, err)
	}
	return append(append([]byte(nil), h...), b...)
}

// decodeOne decodes exactly one element and asserts it consumed the whole input.
func decodeOne(t *testing.T, b []byte) Item {
	t.Helper()
	it, n, complete, err := decodeElement(b)
	if err != nil {
		t.Fatalf("decodeElement: %v", err)
	}
	if !complete {
		t.Fatalf("decodeElement incomplete on full input %x", b)
	}
	if n != len(b) {
		t.Fatalf("decodeElement consumed %d of %d bytes", n, len(b))
	}
	return it
}

// TestBooleanIsTwoBytes pins the FastTransfer rule that PT_BOOLEAN is a u16 on
// the wire (it is 1 byte elsewhere in MAPI). A wrong width desynchronises every
// following property.
func TestBooleanIsTwoBytes(t *testing.T) {
	wire := encodeOne(t, StreamProp{Tag: tag(0x0E1B, mapi.PtBoolean), Value: true})
	want := []byte{0x0B, 0x00, 0x1B, 0x0E, 0x01, 0x00} // propdef LE, then 2-byte boolean
	if !bytes.Equal(wire, want) {
		t.Fatalf("boolean wire\n got %x\nwant %x", wire, want)
	}
	it := decodeOne(t, wire)
	if it.Prop == nil || it.Prop.Value != true {
		t.Fatalf("boolean decode: %+v", it.Prop)
	}
}

// TestUnicodeLengthIncludesNUL pins that PT_UNICODE carries a u32 byte length
// that INCLUDES the 2-byte terminator, and the payload is UTF-16LE.
func TestUnicodeLengthIncludesNUL(t *testing.T) {
	h, b, err := encodeProp(StreamProp{Tag: tag(0x0037, mapi.PtUnicode), Value: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	// propdef (0x0037, 0x001F) LE, then u32 length = 6 (4 bytes "Hi" + 2 NUL).
	wantHeader := []byte{0x1F, 0x00, 0x37, 0x00, 0x06, 0x00, 0x00, 0x00}
	if !bytes.Equal(h, wantHeader) {
		t.Fatalf("unicode header\n got %x\nwant %x", h, wantHeader)
	}
	wantBody := []byte{0x48, 0x00, 0x69, 0x00, 0x00, 0x00} // H i NUL
	if !bytes.Equal(b, wantBody) {
		t.Fatalf("unicode body\n got %x\nwant %x", b, wantBody)
	}
	if it := decodeOne(t, append(h, b...)); it.Prop.Value != "Hi" {
		t.Fatalf("unicode decode: %q", it.Prop.Value)
	}
}

// TestMessageClassForcedString8 pins that PR_MESSAGE_CLASS is always written
// PT_STRING8 (code-page bytes + NUL), even though its tag is PT_UNICODE.
func TestMessageClassForcedString8(t *testing.T) {
	wire := encodeOne(t, StreamProp{Tag: mapi.PrMessageClass, Value: "IPM.Note"})
	// propdef must carry PT_STRING8 (0x001E), not PT_UNICODE.
	wantHead := []byte{0x1E, 0x00, 0x1A, 0x00, 0x09, 0x00, 0x00, 0x00} // type 001E, id 001A, len 9
	if !bytes.HasPrefix(wire, wantHead) {
		t.Fatalf("message-class header\n got %x\nwant prefix %x", wire, wantHead)
	}
	body := wire[len(wantHead):]
	if !bytes.Equal(body, append([]byte("IPM.Note"), 0)) {
		t.Fatalf("message-class body %x", body)
	}
	it := decodeOne(t, wire)
	if it.Prop.Tag.Type() != mapi.PtString8 || it.Prop.Value != "IPM.Note" {
		t.Fatalf("message-class decode: tag=%s val=%q", it.Prop.Tag, it.Prop.Value)
	}
}

// TestMetaTagIdsetGivenTrap pins the one tag whose on-wire type field lies: a
// PT_LONG propdef word (0x40170003) introduces a PT_BINARY body. A reader keying
// on the type field would mis-read it as a 4-byte long.
func TestMetaTagIdsetGivenTrap(t *testing.T) {
	data := []byte{
		0x03, 0x00, 0x17, 0x40, // word 0x40170003 (PT_LONG type field)
		0x04, 0x00, 0x00, 0x00, // u32 binary length 4
		0xDE, 0xAD, 0xBE, 0xEF, // the IDSET bytes
	}
	it := decodeOne(t, data)
	if it.Prop == nil {
		t.Fatal("expected a property, got a marker")
	}
	if it.Prop.Tag.Type() != mapi.PtBinary {
		t.Errorf("trap value type = %s, want PtBinary", it.Prop.Tag.Type())
	}
	if it.Prop.Tag.ID() != 0x4017 {
		t.Errorf("trap tag id = %#x, want 0x4017", it.Prop.Tag.ID())
	}
	if b, _ := it.Prop.Value.([]byte); !bytes.Equal(b, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("trap body = %x", it.Prop.Value)
	}
}

// TestCodepageFlagDecode pins that a propdef type with the 0x8000 code-page flag
// set to CP_UTF16 (1200) is read as a PT_UNICODE string.
func TestCodepageFlagDecode(t *testing.T) {
	// type word = 0x8000 | 1200 = 0x84B0, propid 0x0037 -> word 0x003784B0.
	data := []byte{
		0xB0, 0x84, 0x37, 0x00, // propdef LE
		0x06, 0x00, 0x00, 0x00, // u32 length 6
		0x48, 0x00, 0x69, 0x00, 0x00, 0x00, // "Hi" UTF-16LE + NUL
	}
	it := decodeOne(t, data)
	if it.Prop.Tag.Type() != mapi.PtUnicode || it.Prop.Value != "Hi" {
		t.Fatalf("codepage decode: tag=%s val=%q", it.Prop.Tag, it.Prop.Value)
	}
}

// TestNamedPropInline round-trips both named-property kinds, asserting the
// MnidString name is naked double-NUL-terminated UTF-16LE (no length prefix).
func TestNamedPropInline(t *testing.T) {
	g := mapi.GUID{Data1: 0x11223344, Data2: 0x5566, Data3: 0x7788, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}

	idName := &mapi.PropertyName{Kind: mapi.MnidID, GUID: g, LID: 0x1234}
	wire := encodeOne(t, StreamProp{Tag: tag(0x8005, mapi.PtLong), Name: idName, Value: int32(7)})
	// propdef(4) + GUID(16) + kind(1) + LID(4) + value(4) = 29 bytes.
	if len(wire) != 29 || wire[20] != mapi.MnidID {
		t.Fatalf("MnidID named-prop wire %x", wire)
	}
	it := decodeOne(t, wire)
	if it.Prop.Name == nil || it.Prop.Name.Kind != mapi.MnidID || it.Prop.Name.LID != 0x1234 || it.Prop.Name.GUID != g {
		t.Fatalf("MnidID name decode: %+v", it.Prop.Name)
	}

	strName := &mapi.PropertyName{Kind: mapi.MnidString, GUID: g, Name: "AB"}
	wire = encodeOne(t, StreamProp{Tag: tag(0x8006, mapi.PtLong), Name: strName, Value: int32(9)})
	// name is "AB" UTF-16LE + 00 00, with NO length prefix.
	wantName := []byte{0x41, 0x00, 0x42, 0x00, 0x00, 0x00}
	if !bytes.Contains(wire, wantName) {
		t.Fatalf("MnidString name not naked double-NUL UTF-16: %x", wire)
	}
	it = decodeOne(t, wire)
	if it.Prop.Name == nil || it.Prop.Name.Kind != mapi.MnidString || it.Prop.Name.Name != "AB" {
		t.Fatalf("MnidString name decode: %+v", it.Prop.Name)
	}
}

// TestMarkerRoundTrip verifies a marker word decodes as a marker, not a propdef.
func TestMarkerRoundTrip(t *testing.T) {
	data := []byte{0x03, 0x00, 0x09, 0x40} // STARTTOPFLD 0x40090003
	it := decodeOne(t, data)
	if !it.IsMarker || it.Marker != markerStartTopFld {
		t.Fatalf("marker decode: %+v", it)
	}
}

// TestValueRoundTrips encodes then decodes each value type and asserts the Go
// value survives unchanged.
func TestValueRoundTrips(t *testing.T) {
	g := mapi.GUID{Data1: 0xDEADBEEF, Data2: 1, Data3: 2, Data4: [8]byte{3, 4, 5, 6, 7, 8, 9, 10}}
	cases := []StreamProp{
		{Tag: tag(0x1000, mapi.PtShort), Value: int16(-5)},
		{Tag: tag(0x1001, mapi.PtLong), Value: int32(-123456)},
		{Tag: tag(0x1002, mapi.PtError), Value: uint32(0x80004005)},
		{Tag: tag(0x1003, mapi.PtFloat), Value: float32(3.5)},
		{Tag: tag(0x1004, mapi.PtDouble), Value: float64(2.718281828)},
		{Tag: tag(0x1005, mapi.PtBoolean), Value: false},
		{Tag: tag(0x1006, mapi.PtCurrency), Value: int64(-1)},
		{Tag: tag(0x1007, mapi.PtI8), Value: int64(0x0102030405060708)},
		{Tag: tag(0x1008, mapi.PtSysTime), Value: uint64(0x01D9ABCDEF012345)},
		{Tag: tag(0x1009, mapi.PtCLSID), Value: g},
		{Tag: tag(0x100A, mapi.PtUnicode), Value: "héllo — ünïcode"},
		{Tag: tag(0x100B, mapi.PtBinary), Value: []byte{0, 1, 2, 250, 255}},
		{Tag: tag(0x100C, mapi.PtMvLong), Value: []int32{1, -2, 3}},
		{Tag: tag(0x100D, mapi.PtMvUnicode), Value: []string{"a", "bb", ""}},
		{Tag: tag(0x100E, mapi.PtMvBinary), Value: [][]byte{{1, 2}, {}, {9}}},
		{Tag: tag(0x100F, mapi.PtMvShort), Value: []int16{7, 8}},
	}
	for _, c := range cases {
		it := decodeOne(t, encodeOne(t, c))
		if it.Prop == nil {
			t.Errorf("%s decoded as marker", c.Tag)
			continue
		}
		if it.Prop.Tag != c.Tag {
			t.Errorf("%s round-trip tag = %s", c.Tag, it.Prop.Tag)
		}
		if !reflect.DeepEqual(it.Prop.Value, c.Value) {
			t.Errorf("%s round-trip value = %#v, want %#v", c.Tag, it.Prop.Value, c.Value)
		}
	}
}

// TestUnsupportedTypeErrors verifies the codec refuses (rather than silently
// skipping) a type with no FastTransfer form, so the caller excludes it.
func TestUnsupportedTypeErrors(t *testing.T) {
	if _, _, err := encodeProp(StreamProp{Tag: tag(0x6500, mapi.PtRestriction), Value: []byte{1}}); !errors.Is(err, errUnsupportedFXType) {
		t.Errorf("PtRestriction encode err = %v, want errUnsupportedFXType", err)
	}
}
