package ext

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/mapi"
)

func TestTypedPropValRoundTrip(t *testing.T) {
	tv := mapi.TypedPropVal{Type: mapi.PtLong, Value: int32(0x11223344)}
	p := NewPush(0)
	if err := p.TypedPropVal(tv); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{0x03, 0x00, 0x44, 0x33, 0x22, 0x11} // type PtLong, then int32 LE
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).TypedPropVal()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got.Type != tv.Type || got.Value.(int32) != tv.Value.(int32) {
		t.Fatalf("round-trip = %+v, want %+v", got, tv)
	}
}

func TestSVREIDBinaryRoundTrip(t *testing.T) {
	s := mapi.SVREID{Bin: []byte{0xAA, 0xBB, 0xCC}}
	p := NewPush(0)
	if err := p.SVREID(s); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{0x04, 0x00, 0x00, 0xAA, 0xBB, 0xCC} // length cb+1=4, flag 0, bytes
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).SVREID()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !bytes.Equal(got.Bin, s.Bin) {
		t.Fatalf("Bin = % X, want % X", got.Bin, s.Bin)
	}
}

func TestSVREIDEmptyBinStaysBinary(t *testing.T) {
	s := mapi.SVREID{Bin: []byte{}}
	p := NewPush(0)
	if err := p.SVREID(s); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{0x01, 0x00, 0x00} // length 1 (flag byte only), flag 0
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).SVREID()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got.Bin == nil {
		t.Fatal("empty binary form decoded to nil Bin (would flip to long-term form)")
	}
}

func TestSVREIDLongTermRoundTrip(t *testing.T) {
	s := mapi.SVREID{
		FolderID:  mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x0D}),
		MessageID: mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x2A}),
		Instance:  7,
	}
	p := NewPush(0)
	if err := p.SVREID(s); err != nil {
		t.Fatalf("push: %v", err)
	}
	if p.Len() != 23 { // u16 length + flag + 8 + 8 + 4
		t.Fatalf("length = %d, want 23", p.Len())
	}
	if p.Bytes()[0] != 21 || p.Bytes()[2] != 1 {
		t.Fatalf("header = % X, want length 21 / flag 1", p.Bytes()[:3])
	}
	got, err := NewPull(p.Bytes(), 0).SVREID()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got.Bin != nil || got.FolderID != s.FolderID || got.MessageID != s.MessageID || got.Instance != s.Instance {
		t.Fatalf("round-trip = %+v, want %+v", got, s)
	}
}

func TestSVREIDBadLongTermLength(t *testing.T) {
	// ours=1 but length != 21 must be rejected.
	p := NewPush(0)
	p.Uint16(20)
	p.Uint8(1)
	if _, err := NewPull(p.Bytes(), 0).SVREID(); !errors.Is(err, ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat", err)
	}
}

func TestFlaggedConcreteType(t *testing.T) {
	tag := mapi.MakeTag(0x1234, mapi.PtLong)

	// Available.
	p := NewPush(0)
	if err := p.FlaggedPropVal(tag, mapi.FlaggedPropVal{Flag: mapi.FlaggedAvailable, Value: int32(42)}); err != nil {
		t.Fatalf("push available: %v", err)
	}
	if want := []byte{0x00, 0x2A, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("available bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).FlaggedPropVal(mapi.PtLong)
	if err != nil || got.Flag != mapi.FlaggedAvailable || got.Value.(int32) != 42 {
		t.Fatalf("available round-trip = %+v, err %v", got, err)
	}

	// Unavailable.
	p = NewPush(0)
	if err := p.FlaggedPropVal(tag, mapi.FlaggedPropVal{Flag: mapi.FlaggedUnavailable}); err != nil {
		t.Fatalf("push unavailable: %v", err)
	}
	if want := []byte{0x01}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("unavailable bytes = % X, want % X", p.Bytes(), want)
	}
	got, err = NewPull(p.Bytes(), 0).FlaggedPropVal(mapi.PtLong)
	if err != nil || got.Flag != mapi.FlaggedUnavailable || got.Value != nil {
		t.Fatalf("unavailable round-trip = %+v, err %v", got, err)
	}

	// Error.
	p = NewPush(0)
	if err := p.FlaggedPropVal(tag, mapi.FlaggedPropVal{Flag: mapi.FlaggedError, Value: uint32(0x8004010F)}); err != nil {
		t.Fatalf("push error: %v", err)
	}
	if want := []byte{0x0A, 0x0F, 0x01, 0x04, 0x80}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("error bytes = % X, want % X", p.Bytes(), want)
	}
	got, err = NewPull(p.Bytes(), 0).FlaggedPropVal(mapi.PtLong)
	if err != nil || got.Flag != mapi.FlaggedError || got.Value.(uint32) != 0x8004010F {
		t.Fatalf("error round-trip = %+v, err %v", got, err)
	}
}

func TestFlaggedWithType(t *testing.T) {
	tag := mapi.MakeTag(0x1234, mapi.PtUnspecified)

	// Available: an explicit type precedes the flag.
	av := mapi.FlaggedPropVal{Flag: mapi.FlaggedAvailable, Type: mapi.PtLong, Value: int32(7)}
	p := NewPush(0)
	if err := p.FlaggedPropVal(tag, av); err != nil {
		t.Fatalf("push available: %v", err)
	}
	if want := []byte{0x03, 0x00, 0x00, 0x07, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("available bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).FlaggedPropVal(mapi.PtUnspecified)
	if err != nil || got != av {
		t.Fatalf("available round-trip = %+v, want %+v, err %v", got, av, err)
	}

	// Error: the wire type is forced to PtError.
	er := mapi.FlaggedPropVal{Flag: mapi.FlaggedError, Type: mapi.PtError, Value: uint32(5)}
	p = NewPush(0)
	if err := p.FlaggedPropVal(tag, er); err != nil {
		t.Fatalf("push error: %v", err)
	}
	if want := []byte{0x0A, 0x00, 0x0A, 0x05, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("error bytes = % X, want % X", p.Bytes(), want)
	}
	got, err = NewPull(p.Bytes(), 0).FlaggedPropVal(mapi.PtUnspecified)
	if err != nil || got != er {
		t.Fatalf("error round-trip = %+v, want %+v, err %v", got, er, err)
	}

	// Unavailable: the wire type is 0.
	un := mapi.FlaggedPropVal{Flag: mapi.FlaggedUnavailable}
	p = NewPush(0)
	if err := p.FlaggedPropVal(tag, un); err != nil {
		t.Fatalf("push unavailable: %v", err)
	}
	if want := []byte{0x00, 0x00, 0x01}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("unavailable bytes = % X, want % X", p.Bytes(), want)
	}
	got, err = NewPull(p.Bytes(), 0).FlaggedPropVal(mapi.PtUnspecified)
	if err != nil || got != un {
		t.Fatalf("unavailable round-trip = %+v, want %+v, err %v", got, un, err)
	}
}

func TestFlaggedABKRejected(t *testing.T) {
	tag := mapi.MakeTag(0x1234, mapi.PtUnspecified)
	p := NewPush(FlagABK)
	err := p.FlaggedPropVal(tag, mapi.FlaggedPropVal{Flag: mapi.FlaggedAvailable, Type: mapi.PtLong, Value: int32(1)})
	if !errors.Is(err, ErrFormat) {
		t.Fatalf("ABK push err = %v, want ErrFormat", err)
	}
}

// TestPropValueDispatch proves the generic property-value codec now routes
// PtUnspecified to TYPED_PROPVAL and PtSvrEID to SVREID.
func TestPropValueDispatch(t *testing.T) {
	// PtUnspecified -> TypedPropVal.
	p := NewPush(0)
	if err := p.PropValue(mapi.PtUnspecified, mapi.TypedPropVal{Type: mapi.PtShort, Value: int16(9)}); err != nil {
		t.Fatalf("push unspecified: %v", err)
	}
	v, err := NewPull(p.Bytes(), 0).PropValue(mapi.PtUnspecified)
	if err != nil {
		t.Fatalf("pull unspecified: %v", err)
	}
	if tv, ok := v.(mapi.TypedPropVal); !ok || tv.Type != mapi.PtShort || tv.Value.(int16) != 9 {
		t.Fatalf("unspecified dispatch = %#v", v)
	}

	// PtSvrEID -> SVREID.
	p = NewPush(0)
	if err := p.PropValue(mapi.PtSvrEID, mapi.SVREID{Bin: []byte{0x01, 0x02}}); err != nil {
		t.Fatalf("push svreid: %v", err)
	}
	v, err = NewPull(p.Bytes(), 0).PropValue(mapi.PtSvrEID)
	if err != nil {
		t.Fatalf("pull svreid: %v", err)
	}
	if s, ok := v.(mapi.SVREID); !ok || !bytes.Equal(s.Bin, []byte{0x01, 0x02}) {
		t.Fatalf("svreid dispatch = %#v", v)
	}
}
