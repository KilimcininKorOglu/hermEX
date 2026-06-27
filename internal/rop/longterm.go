package rop

import (
	"fmt"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropLongTermIdFromId handles RopLongTermIdFromId ([MS-OXCSTOR] 2.2.1.8): it converts
// a short-term object id (an EID) into a long-term id (the store's replica GUID plus
// the object's global counter), so a client can persist a stable reference to the
// object across sessions. The request handle must resolve to the logon.
func (s *Session) ropLongTermIdFromId(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	objID, err := p.Uint64()
	if err != nil {
		return false
	}
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon || logon.store == nil {
		writeErr(out, ropLongTermIdFromId, hindex, ecNotSupported)
		return true
	}
	guid, gerr := replidToGUID(logon.store, mapi.EID(objID).ReplID())
	if gerr != nil {
		writeErr(out, ropLongTermIdFromId, hindex, ecNotFound)
		return true
	}

	out.Uint8(ropLongTermIdFromId)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.GUID(guid) // DatabaseGuid
	gc := mapi.EID(objID).GCArray()
	out.Raw(gc[:]) // GlobalCounter (6 bytes)
	out.Uint16(0)  // Padding
	return true
}

// ropIdFromLongTermId handles RopIdFromLongTermId ([MS-OXCSTOR] 2.2.1.9): the inverse
// of RopLongTermIdFromId, resolving a long-term id back to its short-term object id.
func (s *Session) ropIdFromLongTermId(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	guid, e1 := p.GUID()
	gcBytes, e2 := p.Raw(6) // GlobalCounter
	_, e3 := p.Uint16()     // Padding
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon || logon.store == nil {
		writeErr(out, ropIdFromLongTermId, hindex, ecNotSupported)
		return true
	}
	replid, ok := guidToReplid(logon.store, guid)
	if !ok {
		writeErr(out, ropIdFromLongTermId, hindex, ecInvalidParam)
		return true
	}
	var gc mapi.GlobCnt
	copy(gc[:], gcBytes)

	out.Uint8(ropIdFromLongTermId)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(mapi.MakeEID(replid, gc)))
	return true
}

// replidToGUID maps a short-term replica id to its database GUID. hermEX is a
// single-replica store: every object id is built with the object replid (1), and the
// logon advertises its own replica id; both resolve to the mailbox's replica GUID,
// the same GUID that stamps the store's source keys.
func replidToGUID(st *objectstore.Store, replid uint16) (mapi.GUID, error) {
	switch replid {
	case 1, privateReplID:
		return st.StoreGUID()
	default:
		return mapi.GUID{}, fmt.Errorf("rop: unknown replica id %d", replid)
	}
}

// guidToReplid maps a database GUID back to its short-term replica id, recognizing
// only the mailbox's own replica, resolved to the object replid (1).
func guidToReplid(st *objectstore.Store, guid mapi.GUID) (uint16, bool) {
	own, err := st.StoreGUID()
	if err != nil {
		return 0, false
	}
	if guid == own {
		return 1, true
	}
	return 0, false
}
