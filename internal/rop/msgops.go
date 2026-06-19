package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ReadFlags ([MS-OXCMSG] 2.2.3.10.1): the request flag byte, with the reserved
// bits masked off, selects one action by exact value (not a per-bit test). v1
// implements the read/unread state change and read-receipt (MDN) generation
// (rfDefault on the unread→read transition, or rfGenerateReceiptOnly, when the
// message requested one and rfSuppressReceipt is not set); the notify-clear bits
// are accepted but no-op, since change notifications are not implemented.
const (
	rfDefault             uint8 = 0x00 // mark the message read
	rfSuppressReceipt     uint8 = 0x01 // mark read without sending a read receipt
	rfClearReadFlag       uint8 = 0x04 // mark the message unread
	rfReserved            uint8 = 0x0A // reserved bits, masked off before dispatch
	rfGenerateReceiptOnly uint8 = 0x10 // send a read receipt only; no state change
	rfClearNotifyRead     uint8 = 0x20 // clear a pending read notification; no state change
	rfClearNotifyUnread   uint8 = 0x40 // clear a pending non-read notification; no state change
)

// ropSetMessageReadFlag handles RopSetMessageReadFlag ([MS-OXCMSG] 2.2.3.10 /
// [MS-OXCROPS] 2.2.7.10). It marks an opened message read (ReadFlags default) or
// unread (rfClearReadFlag). Two-handle, like SaveChangesMessage: the message is
// addressed by the body InputHandleIndex while the common-header handle is the
// echoed ResponseHandleIndex. A private-mailbox logon carries no ClientData on
// the wire, so the response collapses to a single zero byte (ReadStatusChanged
// is only meaningful for public folders).
func (s *Session) ropSetMessageReadFlag(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ihindex2, e1 := p.Uint8() // InputHandleIndex (the message)
	flags, e2 := p.Uint8()    // ReadFlags
	if e1 != nil || e2 != nil {
		return false
	}
	obj := s.get(handleAt(handles, ihindex2))
	if obj == nil || obj.kind != kindMessage || obj.store == nil {
		writeErr(out, ropSetMessageReadFlag, hindex, ecError)
		return true
	}
	// The read-receipt trigger gates on the unread→read transition, so read the
	// prior state before the write: a message already read through another
	// protocol (an IMAP \Seen) leaves PR_READ_RECEIPT_REQUESTED set, and a
	// flag-only gate would fire a spurious receipt when an Outlook client later
	// opens it. (IMAP \Seen itself generates no receipt — read receipts are a
	// ROP-surface feature in v1.)
	wasRead, err := obj.store.GetMessageReadState(obj.messageID)
	if err != nil {
		writeErr(out, ropSetMessageReadFlag, hindex, ecError)
		return true
	}
	// [MS-OXCMSG] 2.2.3.10: dispatch on the whole flag byte (reserved bits
	// masked), not a per-bit test. Only rfDefault/rfSuppressReceipt mark the
	// message read and only rfClearReadFlag (optionally with rfSuppressReceipt)
	// marks it unread; the receipt-only and notify-clear flags change no read
	// state. Write only when the action changes state, so the call is idempotent.
	var read, change, receipt bool
	switch flags &^ rfReserved {
	case rfDefault:
		read, change = true, true
		receipt = !wasRead // only the unread→read transition owes a receipt
	case rfSuppressReceipt:
		read, change = true, true // marked read, receipt explicitly suppressed
	case rfClearReadFlag, rfClearReadFlag | rfSuppressReceipt:
		change = true
	case rfGenerateReceiptOnly:
		receipt = true // send the receipt without changing read state
	}
	if change {
		if err := obj.store.SetMessageReadState(obj.messageID, read); err != nil {
			writeErr(out, ropSetMessageReadFlag, hindex, ecError)
			return true
		}
	}
	if receipt {
		s.maybeReadReceipt(obj.store, obj.messageID)
	}
	out.Uint8(ropSetMessageReadFlag)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // ReadStatusChanged: always 0 for a private-mailbox logon
	return true
}

// ropDeleteMessages handles RopDeleteMessages ([MS-OXCFOLD] 2.2.1.11 /
// [MS-OXCROPS] 2.2.4.11): it deletes each listed message from the folder at the
// input handle. v1 is synchronous (WantAsynchronous is accepted and ignored) and
// reports PartialCompletion when any id could not be deleted.
func (s *Session) ropDeleteMessages(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()              // WantAsynchronous (v1 is always synchronous)
	_, e2 := p.Uint8()              // NotifyNonRead (notifications out of scope)
	ids, e3 := p.Uint64ArrayShort() // MessageIds (EID_ARRAY)
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	folder := s.get(handleAt(handles, hindex))
	if folder == nil || folder.kind != kindFolder || folder.store == nil {
		writeErr(out, ropDeleteMessages, hindex, ecError)
		return true
	}
	var partial uint8
	for _, eid := range ids {
		if err := folder.store.DeleteObject(int64(mapi.EID(eid).GCValue())); err != nil {
			partial = 1
		}
	}
	out.Uint8(ropDeleteMessages)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
	return true
}

// ropMoveCopyMessages handles RopMoveCopyMessages ([MS-OXCFOLD] 2.2.1.6 /
// [MS-OXCROPS] 2.2.4.6): it copies (WantCopy != 0) or moves (WantCopy == 0) each
// listed message from the source folder at the input handle to the destination
// folder at the body handle index. v1 is synchronous and preserves each
// message's flags and received date through the copy; it reports
// PartialCompletion when any id could not be processed. Source and destination
// are folders in the same mailbox, so both run through the source's store.
func (s *Session) ropMoveCopyMessages(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	dhindex, e1 := p.Uint8()        // DestHandleIndex
	ids, e2 := p.Uint64ArrayShort() // MessageIds (EID_ARRAY)
	_, e3 := p.Uint8()              // WantAsynchronous (v1 is always synchronous)
	wantCopy, e4 := p.Uint8()       // WantCopy (0 = move)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	src := s.get(handleAt(handles, hindex))
	dst := s.get(handleAt(handles, dhindex))
	if src == nil || src.kind != kindFolder || src.store == nil || dst == nil || dst.kind != kindFolder {
		writeErr(out, ropMoveCopyMessages, hindex, ecError)
		return true
	}
	// Resolve each message id to its uid within the source folder; the raw
	// round-trip copy needs the uid and carries the original flags and date.
	uidByID := map[int64]uint32{}
	if msgs, err := src.store.ListMessages(src.folderID); err == nil {
		for _, m := range msgs {
			uidByID[m.ID] = m.UID
		}
	}
	var partial uint8
	for _, eid := range ids {
		uid, ok := uidByID[int64(mapi.EID(eid).GCValue())]
		if !ok {
			partial = 1
			continue
		}
		if err := copyStoredMessage(src.store, src.folderID, uid, dst.folderID); err != nil {
			partial = 1
			continue
		}
		if wantCopy == 0 { // move: drop the source copy after a successful copy
			if err := src.store.DeleteMessage(src.folderID, uid); err != nil {
				partial = 1
			}
		}
	}
	out.Uint8(ropMoveCopyMessages)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
	return true
}

// copyStoredMessage copies one message from (srcFolder, uid) into dstFolder,
// preserving its flags and received date by re-filing the raw message under a
// fresh uid — the same primitive the webmail move/copy path uses.
func copyStoredMessage(st *objectstore.Store, srcFolder int64, uid uint32, dstFolder int64) error {
	info, err := st.MessageByUID(srcFolder, uid)
	if err != nil {
		return err
	}
	raw, err := st.GetMessageRaw(srcFolder, uid)
	if err != nil {
		return err
	}
	_, err = st.AppendMessage(dstFolder, raw, info.InternalDate, info.Flags)
	return err
}
