package ext

import (
	"bytes"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

func TestPropTagsLongWidth(t *testing.T) {
	tags := []mapi.PropTag{mapi.MakeTag(0x3001, mapi.PtLong)}
	p := NewPush(0)
	if err := p.PropTagsLong(tags); err != nil {
		t.Fatalf("push: %v", err)
	}
	// The distinguishing feature vs PropTags is the 32-bit count.
	if want := []byte{0x01, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes()[:4], want) {
		t.Fatalf("count = % X, want % X (u32)", p.Bytes()[:4], want)
	}
	got, err := NewPull(p.Bytes(), 0).PropTagsLong()
	if err != nil || !reflect.DeepEqual(got, tags) {
		t.Fatalf("round-trip = %v, want %v, err %v", got, tags, err)
	}

	// Empty encodes a zero u32 count and reads back nil.
	p = NewPush(0)
	if err := p.PropTagsLong(nil); err != nil {
		t.Fatalf("push empty: %v", err)
	}
	if !bytes.Equal(p.Bytes(), []byte{0, 0, 0, 0}) {
		t.Fatalf("empty bytes = % X, want 00 00 00 00", p.Bytes())
	}
}

func TestPropertyValuesLongWidth(t *testing.T) {
	pv := mapi.PropertyValues{{Tag: mapi.MakeTag(0x3001, mapi.PtLong), Value: int32(7)}}
	p := NewPush(0)
	if err := p.PropertyValuesLong(pv); err != nil {
		t.Fatalf("push: %v", err)
	}
	if want := []byte{0x01, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes()[:4], want) {
		t.Fatalf("count = % X, want u32 01 00 00 00", p.Bytes()[:4])
	}
	got, err := NewPull(p.Bytes(), 0).PropertyValuesLong()
	if err != nil || !reflect.DeepEqual(got, pv) {
		t.Fatalf("round-trip = %v, want %v, err %v", got, pv, err)
	}
}

func TestTArraySet(t *testing.T) {
	rows := []mapi.PropertyValues{
		{{Tag: mapi.MakeTag(0x3001, mapi.PtLong), Value: int32(1)}},
		{{Tag: mapi.MakeTag(0x3002, mapi.PtUnicode), Value: "row2"}},
	}
	p := NewPush(0)
	if err := p.TArraySet(rows); err != nil {
		t.Fatalf("push: %v", err)
	}
	// Outer row count is 32-bit; the first inner row's count is 16-bit.
	if want := []byte{0x02, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes()[:4], want) {
		t.Fatalf("outer count = % X, want u32 02 00 00 00", p.Bytes()[:4])
	}
	if want := []byte{0x01, 0x00}; !bytes.Equal(p.Bytes()[4:6], want) {
		t.Fatalf("inner count = % X, want u16 01 00", p.Bytes()[4:6])
	}
	got, err := NewPull(p.Bytes(), 0).TArraySet()
	if err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("round-trip = %v, want %v, err %v", got, rows, err)
	}
}

func TestProblemArrayVector(t *testing.T) {
	probs := []mapi.PropertyProblem{
		{Index: 0x0001, PropTag: mapi.MakeTag(0x3001, mapi.PtLong), Err: 0x80004005},
	}
	p := NewPush(0)
	if err := p.ProblemArray(probs); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{
		0x01, 0x00, // count u16
		0x01, 0x00, // index u16
		0x03, 0x00, 0x01, 0x30, // proptag u32 LE
		0x05, 0x40, 0x00, 0x80, // err u32 LE
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).ProblemArray()
	if err != nil || !reflect.DeepEqual(got, probs) {
		t.Fatalf("round-trip = %v, want %v, err %v", got, probs, err)
	}
}

func TestEIDsWidth(t *testing.T) {
	ids := []mapi.EID{mapi.MakeEID(1, mapi.GlobCnt{0, 0, 0, 0, 0, 0x0D}), mapi.MakeEID(2, mapi.GlobCnt{0, 0, 0, 0, 0, 0x2A})}
	p := NewPush(0)
	if err := p.EIDs(ids); err != nil {
		t.Fatalf("push: %v", err)
	}
	if want := []byte{0x02, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes()[:4], want) {
		t.Fatalf("count = % X, want u32 02 00 00 00", p.Bytes()[:4])
	}
	if p.Len() != 4+2*8 {
		t.Fatalf("length = %d, want %d", p.Len(), 4+2*8)
	}
	got, err := NewPull(p.Bytes(), 0).EIDs()
	if err != nil || !reflect.DeepEqual(got, ids) {
		t.Fatalf("round-trip = %v, want %v, err %v", got, ids, err)
	}
}
