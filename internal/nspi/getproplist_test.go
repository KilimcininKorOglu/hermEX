package nspi

import (
	"testing"

	"hermex/internal/ext"
)

// buildGetPropList frames a GetPropList request: flags + the MId + the code page
// + an empty auxiliary buffer.
func buildGetPropList(mid, codePage uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0)        // flags
	p.Uint32(mid)      // MId
	p.Uint32(codePage) // code page
	p.Uint32(0)        // cb_auxin
	return p.Bytes()
}

// TestGetPropList proves a valid entry MId yields the default address-book
// column set, and the code page is irrelevant (the list is proptags only).
func TestGetPropList(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	p := ext.NewPull(s.GetPropList(buildGetPropList(midBase, 1252)), abkFlags)
	if status := mustU32(t, p, "status"); status != 0 {
		t.Fatalf("status = %#x, want 0", status)
	}
	if result := mustU32(t, p, "result"); result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if m := mustU8(t, p, "tags marker"); m != 0xFF {
		t.Fatalf("tags marker = %#x, want 0xFF", m)
	}
	tags, err := p.PropTagsLong()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != len(defaultColumns) {
		t.Errorf("tags = %d, want %d (default set)", len(tags), len(defaultColumns))
	}
}

// TestGetPropListInvalidMid proves MId 0 and an unknown MId are both reported as
// an invalid object (no entry to list tags for).
func TestGetPropListInvalidMid(t *testing.T) {
	s := testGAL("alice@hermex.test")
	for _, mid := range []uint32{0, 0x9999} {
		p := ext.NewPull(s.GetPropList(buildGetPropList(mid, 1252)), abkFlags)
		mustU32(t, p, "status")
		if result := mustU32(t, p, "result"); result != ecInvalidObject {
			t.Errorf("MId %#x result = %#x, want ecInvalidObject", mid, result)
		}
	}
}
