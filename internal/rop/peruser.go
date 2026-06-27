package rop

import "hermex/internal/ext"

// Per-user read-information ROP opcodes ([MS-OXCROPS] 2.2.3 / [MS-OXCSTOR]
// 2.2.3.10-2.2.3.13). These carry a client's opaque per-user read state for public
// folders. Exchange (and hermEX) keep no such per-folder blob: the read set lives
// elsewhere (hermEX tracks public read state per message), so these ROPs are served
// with their documented minimal semantics. Every hermEX logon is a private mailbox
// (there is no public-store logon path), so only the private-logon branches arise.
const (
	ropGetPerUserLongTermIds   uint8 = 0x60
	ropGetPerUserGuid          uint8 = 0x61
	ropReadPerUserInformation  uint8 = 0x63
	ropWritePerUserInformation uint8 = 0x64
)

// ropGetPerUserLongTermIds handles RopGetPerUserLongTermIds ([MS-OXCSTOR] 2.2.3.10):
// it returns the long-term ids of the public folders the user has per-user read state
// for. A private logon owns no such set, so the response is an empty array.
func (s *Session) ropGetPerUserLongTermIds(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	if _, err := p.GUID(); err != nil { // DatabaseGuid, names the store; unused on a private logon
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.kind != kindLogon {
		writeErr(out, ropGetPerUserLongTermIds, hindex, ecError)
		return true
	}
	out.Uint8(ropGetPerUserLongTermIds)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0) // LongTermIdCount; the private logon has no per-user folder set
	return true
}

// ropGetPerUserGuid handles RopGetPerUserGuid ([MS-OXCSTOR] 2.2.3.11): it maps a
// long-term id back to the database guid that holds the user's per-user read state.
// A private logon holds none, so the lookup reports ecNotFound.
func (s *Session) ropGetPerUserGuid(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	if _, err := p.LongTermID(); err != nil { // LongTermId, the folder whose guid is sought
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.kind != kindLogon {
		writeErr(out, ropGetPerUserGuid, hindex, ecError)
		return true
	}
	writeErr(out, ropGetPerUserGuid, hindex, ecNotFound)
	return true
}

// ropReadPerUserInformation handles RopReadPerUserInformation ([MS-OXCSTOR]
// 2.2.3.12): it reads a chunk of the per-user read-state blob for a public folder.
// hermEX keeps no such blob, so it reports an empty, already-finished read.
func (s *Session) ropReadPerUserInformation(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.LongTermID() // FolderId, the public folder whose blob is read
	_, e2 := p.Uint8()      // Reserved
	_, e3 := p.Uint32()     // DataOffset
	_, e4 := p.Uint16()     // MaxDataSize
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.kind != kindLogon {
		writeErr(out, ropReadPerUserInformation, hindex, ecError)
		return true
	}
	out.Uint8(ropReadPerUserInformation)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(1)  // HasFinished; there is nothing more to read
	out.Uint16(0) // DataSize; the blob is empty
	return true
}

// ropWritePerUserInformation handles RopWritePerUserInformation ([MS-OXCSTOR]
// 2.2.3.13): it writes a chunk of the per-user read-state blob. hermEX persists no
// such blob, so the write is accepted as a no-op. On a private logon a first chunk
// (DataOffset 0) carries a trailing ReplGuid, which is consumed to keep the batch
// framed.
func (s *Session) ropWritePerUserInformation(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.LongTermID() // FolderId
	_, e2 := p.Uint8()      // HasFinished
	offset, e3 := p.Uint32()
	_, e4 := p.BinShort() // Data
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	if offset == 0 {
		if _, err := p.GUID(); err != nil { // ReplGuid, present only on a private logon's first chunk
			return false
		}
	}
	o := s.get(handleAt(handles, hindex))
	if o == nil || o.kind != kindLogon {
		writeErr(out, ropWritePerUserInformation, hindex, ecError)
		return true
	}
	out.Uint8(ropWritePerUserInformation)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}
