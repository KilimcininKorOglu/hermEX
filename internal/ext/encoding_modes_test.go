package ext

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

func TestABKValueSetPresent(t *testing.T) {
	p := NewPush(FlagABK)
	if err := p.PropValue(mapi.PtString8, "hi"); err != nil {
		t.Fatalf("push: %v", err)
	}
	want := []byte{0xFF, 'h', 'i', 0x00} // value-present prefix, then the string
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	v, err := NewPull(p.Bytes(), FlagABK).PropValue(mapi.PtString8)
	if err != nil || v.(string) != "hi" {
		t.Fatalf("round-trip = %v, err %v", v, err)
	}
}

func TestABKValueSetAbsent(t *testing.T) {
	p := NewPush(FlagABK)
	if err := p.PropValue(mapi.PtBinary, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if !bytes.Equal(p.Bytes(), []byte{0x00}) {
		t.Fatalf("absent bytes = % X, want 00", p.Bytes())
	}
	v, err := NewPull(p.Bytes(), FlagABK).PropValue(mapi.PtBinary)
	if err != nil || v != nil {
		t.Fatalf("absent round-trip = %v, err %v", v, err)
	}
}

func TestABKValueSetMultivalue(t *testing.T) {
	p := NewPush(FlagABK)
	if err := p.PropValue(mapi.PtMvLong, []int32{1, 2}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if p.Bytes()[0] != 0xFF {
		t.Fatalf("mv prefix = %#x, want 0xFF", p.Bytes()[0])
	}
	v, err := NewPull(p.Bytes(), FlagABK).PropValue(mapi.PtMvLong)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got := v.([]int32); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("mv round-trip = %v", got)
	}
}

func TestABKBadValueSet(t *testing.T) {
	// A value-set byte other than 0x00 or 0xFF is malformed.
	if _, err := NewPull([]byte{0x01}, FlagABK).PropValue(mapi.PtString8); !errors.Is(err, ErrFormat) {
		t.Fatalf("err = %v, want ErrFormat", err)
	}
}

func TestMVInstanceStrippedToScalar(t *testing.T) {
	mvi := mapi.MviFlag | mapi.PtLong // 0x3003
	p := NewPush(0)
	if err := p.PropValue(mvi, int32(7)); err != nil {
		t.Fatalf("push: %v", err)
	}
	// Stripped to a single PT_LONG: four bytes, no multivalue count.
	if p.Len() != 4 {
		t.Fatalf("length = %d, want 4 (scalar)", p.Len())
	}
	v, err := NewPull(p.Bytes(), 0).PropValue(mvi)
	if err != nil || v.(int32) != 7 {
		t.Fatalf("round-trip = %v, err %v", v, err)
	}
}

func TestTBLLMTString8Cap(t *testing.T) {
	s := strings.Repeat("a", 600)

	capped := NewPush(FlagTBLLMT)
	capped.String8(s)
	if capped.Len() != 510 { // 509 content + NUL
		t.Fatalf("capped length = %d, want 510", capped.Len())
	}
	got, err := NewPull(capped.Bytes(), FlagTBLLMT).String8()
	if err != nil || len(got) != 509 {
		t.Fatalf("capped read len = %d, err %v", len(got), err)
	}

	uncapped := NewPush(0)
	uncapped.String8(s)
	if uncapped.Len() != 601 { // 600 content + NUL
		t.Fatalf("uncapped length = %d, want 601", uncapped.Len())
	}
}

func TestTBLLMTUnicodeCap(t *testing.T) {
	s := strings.Repeat("a", 600)
	p := NewPush(FlagUTF16 | FlagTBLLMT)
	p.Unicode(s)
	if p.Len() != 510 { // 254 content units + NUL unit, *2
		t.Fatalf("capped length = %d, want 510", p.Len())
	}
	got, err := NewPull(p.Bytes(), FlagUTF16|FlagTBLLMT).Unicode()
	if err != nil || len([]rune(got)) != 254 {
		t.Fatalf("capped read runes = %d, err %v", len([]rune(got)), err)
	}
}

func TestRPCHeaderExt(t *testing.T) {
	h := mapi.RPCHeaderExt{Version: 1, Flags: mapi.RHEFlagLast, Size: 0x10, SizeActual: 0x20}
	p := NewPush(0)
	p.RPCHeaderExt(h)
	want := []byte{0x01, 0x00, 0x04, 0x00, 0x10, 0x00, 0x20, 0x00}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("bytes = % X, want % X", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).RPCHeaderExt()
	if err != nil || got != h {
		t.Fatalf("round-trip = %+v, want %+v, err %v", got, h, err)
	}
}
