package mapi

import "encoding/binary"

// FlatUID is a GUID in its flat 16-byte wire layout: Data1-Data3 little-endian
// followed by the eight trailing bytes verbatim. It is the exact byte form the
// serializer emits for a GUID, and several entry-id structures carry their
// provider identifier in this raw form rather than as structured fields.
type FlatUID [16]byte

// Flat converts a GUID to its flat 16-byte form.
func (g GUID) Flat() FlatUID {
	var f FlatUID
	binary.LittleEndian.PutUint32(f[0:4], g.Data1)
	binary.LittleEndian.PutUint16(f[4:6], g.Data2)
	binary.LittleEndian.PutUint16(f[6:8], g.Data3)
	copy(f[8:16], g.Data4[:])
	return f
}

// GUID converts a flat 16-byte form back into a structured GUID.
func (f FlatUID) GUID() GUID {
	var g GUID
	g.Data1 = binary.LittleEndian.Uint32(f[0:4])
	g.Data2 = binary.LittleEndian.Uint16(f[4:6])
	g.Data3 = binary.LittleEndian.Uint16(f[6:8])
	copy(g.Data4[:], f[8:16])
	return g
}
