package rop

import (
	"errors"

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

// ropTransportSend handles RopTransportSend ([MS-OXOMSG] 2.2.3.2 / [MS-OXCROPS]
// 2.2.7.5): the client composed a message and asks the server to transmit it
// directly. It shares RopSubmitMessage's export+deliver core (deliverComposed)
// but, faithfully to the reference, does NOT file a Sent Items copy or consume the
// draft — those are the submit path's job. The request carries no fields beyond
// the common header; it resolves the message handle, like submit.
//
// On success the response is the head plus NoPropertiesReturned=1; v1 returns no
// property list (the reference returns the sender/representing identity props, a
// documented refinement). On error it is the bare head. The precondition error
// codes follow hermEX's submit path (ecNotFound / ecNotSupported) for consistency,
// where the reference uses ecNullObject / ecAccessDenied — an error-path-only
// deviation. Because TransportSend does not consume the draft, a repeated call
// re-sends; the reference guards that with MSGFLAG_SUBMITTED, which hermEX's
// compose path does not yet model — an accepted v1 gap.
func (s *Session) ropTransportSend(_ *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	obj := s.get(handleAt(handles, hindex))
	if obj == nil || obj.kind != kindNewMessage || obj.newMsg == nil {
		writeErr(out, ropTransportSend, hindex, ecNotFound)
		return true
	}
	// Sending is governed by send-on-behalf: an owner sends as itself, a delegate only
	// when designated on the mailbox's delegate list. The resolved identities are
	// stamped into the transmitted copy.
	representing, sender, allowed, err := s.delegateSendIdentity(obj.store)
	if err != nil {
		writeErr(out, ropTransportSend, hindex, ecError)
		return true
	}
	if !allowed {
		writeErr(out, ropTransportSend, hindex, ecAccessDenied)
		return true
	}
	nm := obj.newMsg
	if !nm.saved || nm.savedID == 0 || s.accounts == nil {
		writeErr(out, ropTransportSend, hindex, ecNotSupported)
		return true
	}
	if _, err = s.deliverComposed(nm, representing, sender); err != nil {
		if errors.Is(err, errNoRecipient) {
			writeErr(out, ropTransportSend, hindex, ecNotFound)
		} else {
			writeErr(out, ropTransportSend, hindex, ecError)
		}
		return true
	}
	out.Uint8(ropTransportSend)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(1) // NoPropertiesReturned: v1 returns no property list
	return true
}
