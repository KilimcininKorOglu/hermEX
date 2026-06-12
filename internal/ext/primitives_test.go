package ext

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/mapi"
)

func TestIntegerWireBytes(t *testing.T) {
	p := NewPush(0)
	p.Uint16(0x0102)
	p.Uint32(0x01020304)
	p.Uint64(0x0102030405060708)
	want := []byte{
		0x02, 0x01,
		0x04, 0x03, 0x02, 0x01,
		0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("integers = % x, want % x", p.Bytes(), want)
	}
	r := NewPull(p.Bytes(), 0)
	if v, _ := r.Uint16(); v != 0x0102 {
		t.Errorf("Uint16 = 0x%04X", v)
	}
	if v, _ := r.Uint32(); v != 0x01020304 {
		t.Errorf("Uint32 = 0x%08X", v)
	}
	if v, _ := r.Uint64(); v != 0x0102030405060708 {
		t.Errorf("Uint64 = 0x%016X", v)
	}
	if r.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0", r.Remaining())
	}
}

func TestBoolWire(t *testing.T) {
	p := NewPush(0)
	p.Bool(true)
	p.Bool(false)
	if !bytes.Equal(p.Bytes(), []byte{1, 0}) {
		t.Fatalf("bool bytes = % x, want 01 00", p.Bytes())
	}
	// A byte above 1 is malformed.
	if _, err := NewPull([]byte{2}, 0).Bool(); !errors.Is(err, ErrFormat) {
		t.Errorf("Bool(2) err = %v, want ErrFormat", err)
	}
}

func TestGUIDWire(t *testing.T) {
	// PSETID_Appointment {00062002-0000-0000-C000-000000000046}; Data1-3 are
	// little-endian on the wire, Data4 is verbatim.
	g := mapi.GUID{Data1: 0x00062002, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	p := NewPush(0)
	p.GUID(g)
	want := []byte{0x02, 0x20, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0, 0, 0, 0, 0, 0, 0x46}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("GUID = % x, want % x", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), 0).GUID()
	if err != nil || got != g {
		t.Errorf("GUID round trip = %v (%v), want %v", got, err, g)
	}
}

func TestBinWidthRegimes(t *testing.T) {
	blob := []byte{0xAA, 0xBB}
	// Default: 16-bit length prefix.
	p := NewPush(0)
	if err := p.Bin(blob); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Bytes(), []byte{0x02, 0x00, 0xAA, 0xBB}) {
		t.Errorf("16-bit Bin = % x", p.Bytes())
	}
	// FlagWCount: 32-bit length prefix.
	pw := NewPush(FlagWCount)
	if err := pw.Bin(blob); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pw.Bytes(), []byte{0x02, 0x00, 0x00, 0x00, 0xAA, 0xBB}) {
		t.Errorf("32-bit Bin = % x", pw.Bytes())
	}
	// A blob too large for a 16-bit prefix is rejected.
	big := make([]byte, 0x10000)
	if err := NewPush(0).Bin(big); !errors.Is(err, ErrFormat) {
		t.Errorf("oversized Bin err = %v, want ErrFormat", err)
	}
	// Round trip under each regime.
	for _, fl := range []Flags{0, FlagWCount} {
		w := NewPush(fl)
		_ = w.Bin(blob)
		got, err := NewPull(w.Bytes(), fl).Bin()
		if err != nil || !bytes.Equal(got, blob) {
			t.Errorf("Bin round trip flags=%d = % x (%v)", fl, got, err)
		}
	}
}

func TestUnicodeUTF16Wire(t *testing.T) {
	// "Hi" as UTF-16LE with the 00 00 terminator.
	p := NewPush(FlagUTF16)
	p.Unicode("Hi")
	want := []byte{0x48, 0x00, 0x69, 0x00, 0x00, 0x00}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("UTF16 'Hi' = % x, want % x", p.Bytes(), want)
	}
	got, err := NewPull(p.Bytes(), FlagUTF16).Unicode()
	if err != nil || got != "Hi" {
		t.Errorf("UTF16 round trip = %q (%v)", got, err)
	}
	// Empty string is just the terminator.
	pe := NewPush(FlagUTF16)
	pe.Unicode("")
	if !bytes.Equal(pe.Bytes(), []byte{0x00, 0x00}) {
		t.Errorf("UTF16 empty = % x, want 00 00", pe.Bytes())
	}
}

func TestUnicodeUTF8Wire(t *testing.T) {
	// Without FlagUTF16, Unicode is a UTF-8 NUL-terminated string.
	p := NewPush(0)
	p.Unicode("Hi")
	if !bytes.Equal(p.Bytes(), []byte{0x48, 0x69, 0x00}) {
		t.Fatalf("UTF8 'Hi' = % x, want 48 69 00", p.Bytes())
	}
	got, err := NewPull(p.Bytes(), 0).Unicode()
	if err != nil || got != "Hi" {
		t.Errorf("UTF8 round trip = %q (%v)", got, err)
	}
}

func TestString8Unterminated(t *testing.T) {
	if _, err := NewPull([]byte{0x48, 0x69}, 0).String8(); !errors.Is(err, ErrFormat) {
		t.Errorf("unterminated String8 err = %v, want ErrFormat", err)
	}
}

func TestPullUnderflow(t *testing.T) {
	if _, err := NewPull([]byte{0x01, 0x02}, 0).Uint32(); !errors.Is(err, ErrUnderflow) {
		t.Errorf("short Uint32 err = %v, want ErrUnderflow", err)
	}
}
