package mapi

import (
	"encoding/binary"
	"math/bits"
)

// GlobCnt is a 6-byte global counter: the low 48 bits of a folder, message, or
// change value encoded big-endian (most-significant byte first), so that
// byte-by-byte comparison reflects chronological order, per MS-OXCFXICS
// §3.1.5.3.
type GlobCnt [6]byte

// EID is an Exchange-level entry identifier value (MS-OXCDATA §2.2.1.2). Its
// 64-bit layout is mixed-endian: a byte-swapped 48-bit global counter in the
// high bits ORed with the 16-bit replica id in the low bits. Build one with
// MakeEID/MakeEIDEx and read its parts through the accessors; the raw integer is
// not meaningful on its own.
type EID uint64

// gcvMask isolates the 48-bit global-counter value after the byte swap
// (eid_t::GCV_MASK).
const gcvMask uint64 = 0x0000FFFFFFFFFFFF

// ValueToGC encodes the low 48 bits of value as a 6-byte big-endian GlobCnt
// (rop_util_value_to_gc).
func ValueToGC(value uint64) GlobCnt {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], value)
	var gc GlobCnt
	copy(gc[:], b[2:8])
	return gc
}

// GCToValue decodes a 6-byte big-endian GlobCnt into a 48-bit value
// (rop_util_gc_to_value).
func GCToValue(gc GlobCnt) uint64 {
	var b [8]byte
	copy(b[2:8], gc[:])
	return binary.BigEndian.Uint64(b[:])
}

// MakeEIDEx composes an EID from a replica id and a value already in
// global-counter-value form (rop_util_make_eid_ex). The value is not masked: the
// caller is responsible for keeping it within 48 bits.
func MakeEIDEx(replid uint16, value uint64) EID {
	return EID(bits.ReverseBytes64(value) | uint64(replid))
}

// MakeEID composes an EID from a replica id and a GlobCnt (rop_util_make_eid).
func MakeEID(replid uint16, gc GlobCnt) EID {
	return MakeEIDEx(replid, GCToValue(gc))
}

// ReplID returns the 16-bit replica id (the low 16 bits, eid_t::replid).
func (e EID) ReplID() uint16 { return uint16(uint64(e) & 0xFFFF) }

// GCValue returns the 48-bit global-counter value (eid_t::gcv /
// rop_util_get_gc_value).
func (e EID) GCValue() uint64 { return bits.ReverseBytes64(uint64(e)) & gcvMask }

// GCArray returns the six global-counter bytes as they sit in the EID's
// little-endian memory image: bytes [2:8] of the little-endian encoding of the
// raw value (rop_util_get_gc_array). This is a distinct computation from
// ValueToGC and is the form embedded in an entry id.
func (e EID) GCArray() GlobCnt {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(e))
	var gc GlobCnt
	copy(gc[:], b[2:8])
	return gc
}
