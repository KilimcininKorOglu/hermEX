package mapi

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// GUID is a 128-bit globally unique identifier (MS-DTYP §2.3.4.2), used for
// PtCLSID property values and named-property namespaces. The field layout
// matches the canonical representation; the on-the-wire encoding (Data1-Data3
// little-endian, Data4 verbatim) lives in package ext.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// String renders the GUID in the canonical 8-4-4-4-12 hex form.
func (g GUID) String() string {
	return fmt.Sprintf("%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		g.Data1, g.Data2, g.Data3,
		g.Data4[0], g.Data4[1], g.Data4[2], g.Data4[3],
		g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// ParseGUID is the inverse of String: it reads the canonical 8-4-4-4-12 hex
// form back into a GUID. Dashes are optional; the input must hold exactly 32
// hex digits. Data1-Data3 are read as big-endian hex (as String prints them),
// Data4 verbatim.
func ParseGUID(s string) (GUID, error) {
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return GUID{}, fmt.Errorf("mapi: parse guid %q: want 32 hex digits, got %d", s, len(clean))
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return GUID{}, fmt.Errorf("mapi: parse guid %q: %w", s, err)
	}
	g := GUID{
		Data1: binary.BigEndian.Uint32(b[0:4]),
		Data2: binary.BigEndian.Uint16(b[4:6]),
		Data3: binary.BigEndian.Uint16(b[6:8]),
	}
	copy(g.Data4[:], b[8:16])
	return g, nil
}
