package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ropSetSpooler handles RopSetSpooler ([MS-OXOMSG] 2.2.1.5 / [MS-OXCROPS] 2.2.7.3):
// a client announces it will spool outgoing mail itself. The request carries no
// fields beyond the common header, and the response is the bare 6-byte head.
// hermEX serves private mailboxes only, so this is a plain acknowledgement; the
// reference's MAPI_E_NO_SUPPORT-for-a-public-logon branch cannot arise here.
func (s *Session) ropSetSpooler(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon {
		writeErr(out, ropSetSpooler, hindex, ecError)
		return true
	}
	out.Uint8(ropSetSpooler)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropGetTransportFolder handles RopGetTransportFolder ([MS-OXOMSG] 2.2.1.9 /
// [MS-OXCROPS] 2.2.7.4): it returns the id of the folder a client deposits
// outgoing messages into — the Outbox. On success the FolderId (as an EID, like
// RopLogon) follows the common head; on error the response is the bare head only.
// The request carries no fields beyond the common header. Private-mailbox only,
// so the reference's public-logon ecNotSupported branch cannot arise here.
func (s *Session) ropGetTransportFolder(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	logon := s.get(handleAt(handles, hindex))
	if logon == nil || logon.kind != kindLogon {
		writeErr(out, ropGetTransportFolder, hindex, ecError)
		return true
	}
	out.Uint8(ropGetTransportFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(mapi.PrivateFIDOutbox)))) // the Outbox
	return true
}
