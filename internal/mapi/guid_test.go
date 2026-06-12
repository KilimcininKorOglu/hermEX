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
