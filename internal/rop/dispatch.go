package rop

import "hermex/internal/ext"

// ROP operation ids ([MS-OXCROPS] 2.2). v1 handles the read-core set.
const (
	ropRelease               uint8 = 0x01
	ropOpenFolder            uint8 = 0x02
	ropOpenMessage           uint8 = 0x03
	ropGetHierarchyTable     uint8 = 0x04
	ropGetContentsTable      uint8 = 0x05
	ropGetPropertiesSpecific uint8 = 0x07
	ropGetPropertiesAll      uint8 = 0x08
	ropSetColumns            uint8 = 0x12
	ropSortTable             uint8 = 0x13
	ropRestrict              uint8 = 0x14
	ropQueryRows             uint8 = 0x15
	ropOpenStream            uint8 = 0x2B
	ropReadStream            uint8 = 0x2C
	ropLogon                 uint8 = 0xFE
)

// MAPI return codes ([MS-OXCDATA] 2.4.1) carried in a ROP response ReturnValue.
const (
	ecSuccess  uint32 = 0x00000000
	ecError    uint32 = 0x80004005 // generic failure / unimplemented ROP
	ecNotFound uint32 = 0x8004010F // MAPI_E_NOT_FOUND (no such folder/object)
)

// Dispatch parses the request ROP list and returns the response ROP bytes plus
// the updated server-handle table, which the RopBuffer codec re-frames. Each
// ROP resolves its handle slot against the table, mutates the session's object
// graph, and appends its response.
//
// ROPs are variable-length with no per-ROP length prefix, so an unrecognized
// ROP cannot be skipped: dispatch emits the 6-byte generic error for it and
// stops, since the remaining ROPs in the batch can no longer be located. A
// short or truncated header likewise ends the batch.
func (s *Session) Dispatch(ropList []byte, reqHandles []uint32) (respRops []byte, respHandles []uint32) {
	handles := append([]uint32(nil), reqHandles...)
	p := ext.NewPull(ropList, ext.FlagUTF16)
	out := ext.NewPush(ext.FlagUTF16)
loop:
	for p.Remaining() > 0 {
		ropID, e1 := p.Uint8()
		_, e2 := p.Uint8() // LogonId (a single logon in v1)
		hindex, e3 := p.Uint8()
		if e1 != nil || e2 != nil || e3 != nil {
			break loop
		}
		switch ropID {
		case ropLogon:
			if !s.ropLogon(p, out, handles, hindex) {
				break loop
			}
		case ropRelease:
			s.ropRelease(handles, hindex)
		case ropOpenFolder:
			if !s.ropOpenFolder(p, out, handles, hindex) {
				break loop
			}
		case ropOpenMessage:
			if !s.ropOpenMessage(p, out, handles, hindex) {
				break loop
			}
		case ropGetPropertiesSpecific:
			if !s.ropGetPropertiesSpecific(p, out, handles, hindex) {
				break loop
			}
		case ropGetPropertiesAll:
			if !s.ropGetPropertiesAll(p, out, handles, hindex) {
				break loop
			}
		case ropGetContentsTable:
			if !s.ropGetContentsTable(p, out, handles, hindex) {
				break loop
			}
		case ropSetColumns:
			if !s.ropSetColumns(p, out, handles, hindex) {
				break loop
			}
		case ropGetHierarchyTable:
			if !s.ropGetHierarchyTable(p, out, handles, hindex) {
				break loop
			}
		case ropSortTable:
			if !s.ropSortTable(p, out, handles, hindex) {
				break loop
			}
		case ropRestrict:
			if !s.ropRestrict(p, out, handles, hindex) {
				break loop
			}
		case ropQueryRows:
			if !s.ropQueryRows(p, out, handles, hindex) {
				break loop
			}
		case ropOpenStream:
			if !s.ropOpenStream(p, out, handles, hindex) {
				break loop
			}
		case ropReadStream:
			if !s.ropReadStream(p, out, handles, hindex) {
				break loop
			}
		default:
			writeErr(out, ropID, hindex, ecError)
			break loop
		}
	}
	return out.Bytes(), handles
}

// writeErr appends the 6-byte generic ROP error response: RopId, HandleIndex, ec.
func writeErr(out *ext.Push, ropID, hindex uint8, ec uint32) {
	out.Uint8(ropID)
	out.Uint8(hindex)
	out.Uint32(ec)
}

// handleAt reads a server-handle-table slot, returning the null handle when the
// index is out of range.
func handleAt(handles []uint32, idx uint8) uint32 {
	if int(idx) < len(handles) {
		return handles[idx]
	}
	return 0xFFFFFFFF
}

// setHandle writes a server-handle-table slot when the index is in range.
func setHandle(handles []uint32, idx uint8, h uint32) {
	if int(idx) < len(handles) {
		handles[idx] = h
	}
}
