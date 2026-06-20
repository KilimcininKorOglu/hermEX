package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// msgStatusInConflict is the PidTagMessageStatus bit a client may not set
// ([MS-OXCMSG] 2.2.1.8 MSGSTATUS_IN_CONFLICT); SetMessageStatus rejects it.
const msgStatusInConflict uint32 = 0x00000800

// ropReloadCachedInformation handles RopReloadCachedInformation ([MS-OXCMSG]
// 2.2.3.4): it re-reads the open message's cached header — the subject strings
// and (v1) an empty recipient table — after the client has changed it. The
// response is byte-identical to RopOpenMessage's tail (without the output
// handle), so a client that refreshes after an edit reads the same shape it got
// when it first opened the message.
func (s *Session) ropReloadCachedInformation(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	if _, err := p.Uint16(); err != nil { // Reserved
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil {
		writeErr(out, ropReloadCachedInfo, hindex, ecError)
		return true
	}
	// readMessageProps serves both an opened store message and an embedded message,
	// so RopReloadCachedInformation refreshes the header of either.
	props, ok, err := msg.readMessageProps(mapi.PrSubjectPrefix, mapi.PrNormalizedSubject)
	if !ok || err != nil {
		writeErr(out, ropReloadCachedInfo, hindex, ecError)
		return true
	}

	out.Uint8(ropReloadCachedInfo)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // HasNamedProperties (v1 advertises none)
	pushTypedString(out, stringProp(props, mapi.PrSubjectPrefix))
	pushTypedString(out, stringProp(props, mapi.PrNormalizedSubject))
	out.Uint16(0)         // RecipientCount (v1: inline recipient table deferred)
	_ = out.PropTags(nil) // RecipientColumns (empty proptag array)
	out.Uint8(0)          // RowCount
	return true
}

// ropGetMessageStatus handles RopGetMessageStatus ([MS-OXCMSG] 2.2.3.9): it
// returns a message's PidTagMessageStatus bits. The status ROPs operate against a
// FOLDER handle and address the message by id (the message need not be open), so
// the handle must resolve to a folder; an unset status reports MAPI_E_NOT_FOUND.
func (s *Session) ropGetMessageStatus(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	msgEID, e1 := p.Uint64() // MessageId
	if e1 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.store == nil {
		writeErr(out, ropGetMessageStatus, hindex, ecError)
		return true
	}
	if folder.kind != kindFolder {
		writeErr(out, ropGetMessageStatus, hindex, ecNotSupported)
		return true
	}
	mid := int64(mapi.EID(msgEID).GCValue())
	props, err := folder.store.GetMessageProperties(mid, mapi.PrMsgStatus)
	if err != nil {
		writeErr(out, ropGetMessageStatus, hindex, ecError)
		return true
	}
	status, ok := messageStatus(props)
	if !ok {
		writeErr(out, ropGetMessageStatus, hindex, ecNotFound)
		return true
	}

	out.Uint8(ropGetMessageStatus)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint32(status)
	return true
}

// ropSetMessageStatus handles RopSetMessageStatus ([MS-OXCMSG] 2.2.3.10): it
// applies the masked status bits to a message's PidTagMessageStatus and returns
// the merged result. Only the masked bits are changed; the unmasked original bits
// are preserved. Setting the in-conflict bit is refused. Like GetMessageStatus it
// resolves a FOLDER handle and addresses the message by id, and it does not
// advance the message's change number (status is a flag set directly, not a saved
// edit).
func (s *Session) ropSetMessageStatus(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	msgEID, e1 := p.Uint64()    // MessageId
	newStatus, e2 := p.Uint32() // MessageStatusFlags
	mask, e3 := p.Uint32()      // MessageStatusMask
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.store == nil {
		writeErr(out, ropSetMessageStatus, hindex, ecError)
		return true
	}
	if folder.kind != kindFolder {
		writeErr(out, ropSetMessageStatus, hindex, ecNotSupported)
		return true
	}
	// Changing a message's status modifies it: a delegate needs EditAny on the folder.
	if s.denyWrite(out, ropSetMessageStatus, hindex, folder.store, folder.folderID, mapi.FrightsEditAny) {
		return true
	}
	mid := int64(mapi.EID(msgEID).GCValue())
	props, err := folder.store.GetMessageProperties(mid, mapi.PrMsgStatus)
	if err != nil {
		writeErr(out, ropSetMessageStatus, hindex, ecError)
		return true
	}
	original, ok := messageStatus(props)
	if !ok {
		writeErr(out, ropSetMessageStatus, hindex, ecNotFound)
		return true
	}
	applied := newStatus & mask
	if applied&msgStatusInConflict != 0 {
		writeErr(out, ropSetMessageStatus, hindex, ecAccessDenied)
		return true
	}
	// Keep the original bits the mask did not select: merged = applied | original
	// with the masked-and-cleared bits removed from original.
	merged := applied | (original &^ (mask &^ applied))
	if err := folder.store.SetMessageProperties(mid, mapi.PropertyValues{{Tag: mapi.PrMsgStatus, Value: int32(merged)}}); err != nil {
		writeErr(out, ropSetMessageStatus, hindex, ecError)
		return true
	}

	out.Uint8(ropSetMessageStatus)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint32(merged)
	return true
}

// messageStatus extracts PidTagMessageStatus (a PtLong) as a uint32, reporting
// whether it was present.
func messageStatus(props mapi.PropertyValues) (uint32, bool) {
	if v, ok := props.Get(mapi.PrMsgStatus); ok {
		if n, ok := v.(int32); ok {
			return uint32(n), true
		}
	}
	return 0, false
}
