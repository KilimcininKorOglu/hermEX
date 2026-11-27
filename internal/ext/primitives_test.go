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
	if v, err := r.Uint16(); err != nil || v != 0x0102 {
		t.Errorf("Uint16 = 0x%04X, %v", v, err)
	}
	if v, err := r.Uint32(); err != nil || v != 0x01020304 {
		t.Errorf("Uint32 = 0x%08X, %v", v, err)
	}
	if v, err := r.Uint64(); err != nil || v != 0x0102030405060708 {
		t.Errorf("Uint64 = 0x%016X, %v", v, err)
	}
	if _, err := r.Uint16(); err == nil {
		t.Error("a read past the end returned no error")
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
		if err := w.Bin(blob); err != nil {
			t.Fatalf("Bin flags=%d: %v", fl, err)
		}
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

// TestUnicodeUnterminated refuses a UTF-16 string with no aligned 00 00
// terminator: none at all, and a 00 00 pair that straddles two code units
// (0x0041 0x4100 holds the bytes 00 00 at an odd offset), which is not one.
func TestUnicodeUnterminated(t *testing.T) {
	for name, buf := range map[string][]byte{
		"no terminator":         {0x48, 0x00, 0x69, 0x00},
		"misaligned zero bytes": {0x41, 0x00, 0x00, 0x41},
	} {
		if _, err := NewPull(buf, FlagUTF16).Unicode(); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
	}
}

// TestSkipPassesAReservedField skips a 4-byte reserved field and reads the
// field after it; a negative count or one past the end is refused and leaves
// the read position where it was.
func TestSkipPassesAReservedField(t *testing.T) {
	p := NewPull([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0x02, 0x01}, 0)
	mustNoErr(t, p.Skip(4), "skip reserved")
	v, err := p.Uint16()
	mustNoErr(t, err, "read after skip")
	wantEq(t, v, uint16(0x0102), "field after the reserved one")

	q := NewPull([]byte{0x01, 0x02, 0x03}, 0)
	for _, n := range []int{-1, 4} {
		if err := q.Skip(n); !errors.Is(err, ErrUnderflow) {
			t.Errorf("Skip(%d) err = %v, want ErrUnderflow", n, err)
		}
		wantEq(t, q.Remaining(), 3, "remaining after a refused skip")
	}
}

// TestSkipBlobsPassTheirPrefixAndBytes skips each length-prefixed blob form
// (MS-OXCDATA 2.11.1: a 16-bit count, or 32-bit under wide counts; BinShort is
// always 16-bit and BinEx always 32-bit) and reads the 0x7E marker byte after
// it. A count past the end of the buffer is refused.
func TestSkipBlobsPassTheirPrefixAndBytes(t *testing.T) {
	cases := []struct {
		name  string
		buf   []byte
		flags Flags
		skip  func(*Pull) error
	}{
		{"Bin 16-bit", []byte{2, 0, 0xAA, 0xBB, 0x7E}, 0, (*Pull).SkipBin},
		{"Bin wide", []byte{2, 0, 0, 0, 0xAA, 0xBB, 0x7E}, FlagWCount, (*Pull).SkipBin},
		{"BinShort", []byte{1, 0, 0xAA, 0x7E}, FlagWCount, (*Pull).SkipBinShort},
		{"BinEx", []byte{1, 0, 0, 0, 0xAA, 0x7E}, 0, (*Pull).SkipBinEx},
	}
	for _, c := range cases {
		p := NewPull(c.buf, c.flags)
		mustNoErr(t, c.skip(p), c.name)
		v, err := p.Uint8()
		mustNoErr(t, err, c.name+" marker")
		wantEq(t, v, uint8(0x7E), c.name+" byte after the blob")

		short := NewPull(c.buf[:len(c.buf)-2], c.flags)
		if err := c.skip(short); !errors.Is(err, ErrUnderflow) {
			t.Errorf("%s truncated: err = %v, want ErrUnderflow", c.name, err)
		}
	}
}

func TestPullUnderflow(t *testing.T) {
	if _, err := NewPull([]byte{0x01, 0x02}, 0).Uint32(); !errors.Is(err, ErrUnderflow) {
		t.Errorf("short Uint32 err = %v, want ErrUnderflow", err)
	}
}
