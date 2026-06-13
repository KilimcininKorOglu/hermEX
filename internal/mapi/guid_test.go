package mapi

import "testing"

// PSETID_Appointment is a real named-property namespace GUID; formatting it
// verifies the canonical 8-4-4-4-12 rendering used throughout MAPI.
func TestGUIDString(t *testing.T) {
	g := GUID{
		Data1: 0x00062002,
		Data2: 0x0000,
		Data3: 0x0000,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
	const want = "00062002-0000-0000-C000-000000000046"
	if got := g.String(); got != want {
		t.Errorf("GUID.String() = %q, want %q", got, want)
	}
}

// TestParseGUIDRoundTrip verifies ParseGUID is the exact inverse of String,
// recovering every field including the byte order of Data1-Data3.
func TestParseGUIDRoundTrip(t *testing.T) {
	want := GUID{
		Data1: 0x01234567,
		Data2: 0x89AB,
		Data3: 0xCDEF,
		Data4: [8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF},
	}
	got, err := ParseGUID(want.String())
	if err != nil {
		t.Fatalf("ParseGUID(%q) error: %v", want.String(), err)
	}
	if got != want {
		t.Errorf("ParseGUID round-trip = %+v, want %+v", got, want)
	}

	// The canonical PSETID_Appointment string parses to its known fields.
	g, err := ParseGUID("00062002-0000-0000-C000-000000000046")
	if err != nil {
		t.Fatalf("ParseGUID(canonical) error: %v", err)
	}
	if g.Data1 != 0x00062002 || g.Data4 != [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46} {
		t.Errorf("ParseGUID(canonical) = %+v", g)
	}
}

// TestParseGUIDInvalid rejects inputs that are not 32 hex digits.
func TestParseGUIDInvalid(t *testing.T) {
	for _, s := range []string{"", "1234", "00062002-0000-0000-C000-00000000004", "ZZ062002-0000-0000-C000-000000000046"} {
		if _, err := ParseGUID(s); err == nil {
			t.Errorf("ParseGUID(%q) = nil error, want failure", s)
		}
	}
}
