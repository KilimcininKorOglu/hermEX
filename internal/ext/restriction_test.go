package ext

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

// roundTripRestriction pushes then pulls r under the given flags and requires an
// identical result. reflect.DeepEqual is mandatory: Restriction holds an any
// (and slices), so == would panic at runtime.
func roundTripRestriction(t *testing.T, flags Flags, r mapi.Restriction) {
	t.Helper()
	p := NewPush(flags)
	if err := p.Restriction(r); err != nil {
		t.Fatalf("push: %v", err)
	}
	got, err := NewPull(p.Bytes(), flags).Restriction()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", got, r)
	}
}

func TestRestrictionPropertyVector(t *testing.T) {
	tag := mapi.MakeTag(0x3001, mapi.PtLong) // 0x30010003
	r := mapi.Restriction{
		Type: mapi.ResProperty,
		Value: mapi.PropertyRestriction{
			Relop:   mapi.RelopEQ,
			PropTag: tag,
			PropVal: mapi.TaggedPropVal{Tag: tag, Value: int32(5)},
		},
	}
	p := NewPush(0)
	if err := p.Restriction(r); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{
		0x04,                   // type ResProperty
		0x04,                   // relop EQ
		0x03, 0x00, 0x01, 0x30, // proptag LE
		0x03, 0x00, 0x01, 0x30, // tagged-propval tag LE
		0x05, 0x00, 0x00, 0x00, // int32 value LE
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	roundTripRestriction(t, 0, r)
}

func TestRestrictionLeaves(t *testing.T) {
	tag := mapi.MakeTag(0x3001, mapi.PtLong)
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		FuzzyLevel: 0x00010001, PropTag: mapi.MakeTag(0x0037, mapi.PtUnicode),
		PropVal: mapi.TaggedPropVal{Tag: mapi.MakeTag(0x0037, mapi.PtUnicode), Value: "hello"},
	}})
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResPropCompare, Value: mapi.ComparePropsRestriction{
		Relop: mapi.RelopNE, PropTag1: tag, PropTag2: mapi.MakeTag(0x3002, mapi.PtLong),
	}})
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResBitmask, Value: mapi.BitmaskRestriction{
		Relop: mapi.BmrNez, PropTag: tag, Mask: 0xDEADBEEF,
	}})
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResSize, Value: mapi.SizeRestriction{
		Relop: mapi.RelopGT, PropTag: mapi.MakeTag(0x0E08, mapi.PtLong), Size: 4096,
	}})
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: tag}})
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResNull})
}

func TestRestrictionAndOrWidth(t *testing.T) {
	child := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.MakeTag(0x3001, mapi.PtLong)}}
	r := mapi.Restriction{Type: mapi.ResAnd, Value: []mapi.Restriction{child}}

	// Default (16-bit child count).
	p := NewPush(0)
	if err := p.Restriction(r); err != nil {
		t.Fatalf("push u16: %v", err)
	}
	if want := []byte{0x00, 0x01, 0x00}; !bytes.Equal(p.Bytes()[:3], want) {
		t.Fatalf("u16 header = % X, want % X", p.Bytes()[:3], want)
	}
	roundTripRestriction(t, 0, r)

	// FlagWCount (32-bit child count).
	p = NewPush(FlagWCount)
	if err := p.Restriction(r); err != nil {
		t.Fatalf("push u32: %v", err)
	}
	if want := []byte{0x00, 0x01, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes()[:5], want) {
		t.Fatalf("u32 header = % X, want % X", p.Bytes()[:5], want)
	}
	roundTripRestriction(t, FlagWCount, r)
}

func TestRestrictionNested(t *testing.T) {
	exist := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.MakeTag(0x3001, mapi.PtLong)}}
	not := mapi.Restriction{Type: mapi.ResNot, Value: exist}
	prop := mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopEQ, PropTag: mapi.MakeTag(0x3002, mapi.PtLong),
		PropVal: mapi.TaggedPropVal{Tag: mapi.MakeTag(0x3002, mapi.PtLong), Value: int32(1)},
	}}
	r := mapi.Restriction{Type: mapi.ResOr, Value: []mapi.Restriction{not, prop}}
	roundTripRestriction(t, 0, r)
	roundTripRestriction(t, FlagWCount, r)
}

func TestRestrictionComment(t *testing.T) {
	pv := mapi.TaggedPropVal{Tag: mapi.MakeTag(0x3001, mapi.PtLong), Value: int32(9)}
	inner := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.MakeTag(0x3001, mapi.PtLong)}}

	// With a child restriction.
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResComment, Value: mapi.CommentRestriction{
		PropVals: []mapi.TaggedPropVal{pv}, Res: &inner,
	}})
	// Without a child (ResAnnotation shares the codec).
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResAnnotation, Value: mapi.CommentRestriction{
		PropVals: []mapi.TaggedPropVal{pv},
	}})
}

func TestRestrictionCommentZeroRejected(t *testing.T) {
	// type ResComment then a zero count must be rejected on pull.
	p := NewPush(0)
	p.Uint8(uint8(mapi.ResComment))
	p.Uint8(0)
	if _, err := NewPull(p.Bytes(), 0).Restriction(); !errors.Is(err, ErrFormat) {
		t.Fatalf("zero-count comment err = %v, want ErrFormat", err)
	}
}

func TestRestrictionSubAndCount(t *testing.T) {
	inner := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.MakeTag(0x3001, mapi.PtLong)}}
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResSub, Value: mapi.SubRestriction{SubObject: 0x000E, Res: inner}})
	roundTripRestriction(t, 0, mapi.Restriction{Type: mapi.ResCount, Value: mapi.CountRestriction{Count: 3, SubRes: inner}})
}

func TestRestrictionPropValueDispatch(t *testing.T) {
	r := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.MakeTag(0x3001, mapi.PtLong)}}
	p := NewPush(0)
	if err := p.PropValue(mapi.PtRestriction, r); err != nil {
		t.Fatalf("push: %v", err)
	}
	v, err := NewPull(p.Bytes(), 0).PropValue(mapi.PtRestriction)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !reflect.DeepEqual(v, r) {
		t.Fatalf("dispatch round-trip = %#v, want %#v", v, r)
	}
}
