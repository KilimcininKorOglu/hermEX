package mapi

import (
	"math/bits"
	"testing"
)

// TestValueToGCBigEndian pins the endianness with a distinct-byte vector: a
// little-endian implementation would reverse these bytes and pass every
// round-trip test, so the explicit ordering is the real check.
func TestValueToGCBigEndian(t *testing.T) {
	gc := ValueToGC(0x010203040506)
	want := GlobCnt{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	if gc != want {
		t.Fatalf("ValueToGC = % X, want % X", gc[:], want[:])
	}
	if v := GCToValue(want); v != 0x010203040506 {
		t.Fatalf("GCToValue = %#x, want 0x010203040506", v)
	}
}

func TestGCValueRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 0xFF, 0x123456789ABC, gcvMask} {
		if got := GCToValue(ValueToGC(v)); got != v {
			t.Fatalf("round-trip %#x = %#x", v, got)
		}
	}
}

// TestEIDInvariants checks the three relations the reference guarantees for an EID
// built from a replica id and a global counter.
func TestEIDInvariants(t *testing.T) {
	replid := uint16(0x1234)
	gc := GlobCnt{0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F}
	e := MakeEID(replid, gc)

	if e.ReplID() != replid {
		t.Fatalf("ReplID = %#x, want %#x", e.ReplID(), replid)
	}
	if e.GCValue() != GCToValue(gc) {
		t.Fatalf("GCValue = %#x, want %#x", e.GCValue(), GCToValue(gc))
	}
	if gca := e.GCArray(); gca != gc {
		t.Fatalf("GCArray = % X, want % X", gca[:], gc[:])
	}
}

// TestMakeEIDExNoMask proves MakeEIDEx does not apply GCV_MASK: a bit above the
// 48-bit boundary survives, where the masking two-argument constructor would
// drop it.
func TestMakeEIDExNoMask(t *testing.T) {
	value := uint64(1) << 48 // bit 48, outside the 48-bit GC range
	got := MakeEIDEx(0, value)
	want := EID(bits.ReverseBytes64(value))
	if got != want {
		t.Fatalf("MakeEIDEx = %#x, want %#x", uint64(got), uint64(want))
	}
	if got == 0 {
		t.Fatal("MakeEIDEx masked the high bit away; it must not")
	}
}

func TestFlatRoundTrip(t *testing.T) {
	g := GUID{
		Data1: 0xAABBCCDD,
		Data2: 0xEEFF,
		Data3: 0x0102,
		Data4: [8]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48},
	}
	f := g.Flat()
	want := FlatUID{
		0xDD, 0xCC, 0xBB, 0xAA, // Data1 little-endian
		0xFF, 0xEE, // Data2 little-endian
		0x02, 0x01, // Data3 little-endian
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, // Data4 verbatim
	}
	if f != want {
		t.Fatalf("Flat = % X, want % X", f[:], want[:])
	}
	if g2 := f.GUID(); g2 != g {
		t.Fatalf("GUID round-trip = %+v, want %+v", g2, g)
	}
}
