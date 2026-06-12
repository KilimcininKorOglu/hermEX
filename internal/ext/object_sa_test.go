package ext

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

func TestPropValueObjectAsBinary(t *testing.T) {
	// PT_OBJECT is carried as a length-prefixed binary outside address-book mode.
	p := NewPush(0)
	if err := p.PropValue(mapi.PtObject, []byte{0xAA, 0xBB}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if want := []byte{0x02, 0x00, 0xAA, 0xBB}; !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	v, err := NewPull(p.Bytes(), 0).PropValue(mapi.PtObject)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !bytes.Equal(v.([]byte), []byte{0xAA, 0xBB}) {
		t.Fatalf("round-trip = % X", v.([]byte))
	}
}

func TestPropValueObjectABKEmpty(t *testing.T) {
	// In address-book mode PT_OBJECT carries no data at all.
	p := NewPush(FlagABK)
	if err := p.PropValue(mapi.PtObject, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if p.Len() != 0 {
		t.Fatalf("ABK object wrote %d bytes, want 0", p.Len())
	}
	v, err := NewPull(p.Bytes(), FlagABK).PropValue(mapi.PtObject)
	if err != nil || v != nil {
		t.Fatalf("ABK object pull = %v, err %v", v, err)
	}
}

func TestUint64ArrayShortWidth(t *testing.T) {
	vs := []uint64{0x1122334455667788, 0x99}
	p := NewPush(0)
	if err := p.Uint64ArrayShort(vs); err != nil {
		t.Fatalf("push: %v", err)
	}
	// 16-bit count (regime exception), then two little-endian uint64s.
	if want := []byte{0x02, 0x00}; !bytes.Equal(p.Bytes()[:2], want) {
		t.Fatalf("count = % X, want u16 02 00", p.Bytes()[:2])
	}
	if p.Len() != 2+2*8 {
		t.Fatalf("length = %d, want %d", p.Len(), 2+2*8)
	}
	got, err := NewPull(p.Bytes(), 0).Uint64ArrayShort()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 2 || got[0] != vs[0] || got[1] != vs[1] {
		t.Fatalf("round-trip = %v, want %v", got, vs)
	}
}
