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
	obj, ok := s.openMessage(out, ropSetMessageReadFlag, handles, ihindex2, hindex)
	if !ok {
		return true
	}
	// Changing a message's read state modifies it: a delegate needs EditAny on the
	// folder (a read-only Reviewer may not flip read state in a shared mailbox).
	if s.denyWrite(out, ropSetMessageReadFlag, hindex, obj.store, obj.folderID, mapi.FrightsEditAny) {
		return true
	}
	// The read-receipt trigger gates on the unread→read transition, so read the
	// prior state before the write: a message already read through another
	// protocol (an IMAP \Seen) leaves PR_READ_RECEIPT_REQUESTED set, and a
	// flag-only gate would fire a spurious receipt when an Outlook client later
	// opens it. (IMAP \Seen itself generates no receipt, read receipts are a
	// ROP-surface feature in v1.)
	wasRead, err := obj.store.GetMessageReadState(obj.messageID)
	if err != nil {
		writeErr(out, ropSetMessageReadFlag, hindex, ecError)
		return true
	}
	// [MS-OXCMSG] 2.2.3.10: dispatch on the whole flag byte (reserved bits
	// masked), not a per-bit test, which decodeReadFlags does. Only the default
	// action owes a receipt here, and only on the unread-to-read transition.
	action := decodeReadFlags(flags)
	if action.change {
		// Only the unread-to-read transition owes a receipt. The receipt-only
		// action does not change state and sends unconditionally.
		action.receipt = action.receipt && !wasRead
		if err := obj.store.SetMessageReadState(obj.messageID, action.read); err != nil {
			writeErr(out, ropSetMessageReadFlag, hindex, ecError)
			return true
		}
	}
	if action.receipt {
		s.maybeReadReceipt(obj.store, obj.messageID)
	}
	out.Uint8(ropSetMessageReadFlag)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // ReadStatusChanged: always 0 for a private-mailbox logon
	return true
}

// ropDeleteMessages handles RopDeleteMessages ([MS-OXCFOLD] 2.2.1.11 /
// [MS-OXCROPS] 2.2.4.11): it soft-deletes each listed message from the folder at
// the input handle into the Recoverable Items dumpster (recoverable until
// retention). v1 is synchronous (WantAsynchronous is accepted and ignored) and
// reports PartialCompletion when any id could not be deleted.
// ropSetReadFlags handles RopSetReadFlags ([MS-OXCMSG] 2.2.3.11 / [MS-OXCROPS]
// 2.2.6.10): the bulk counterpart of RopSetMessageReadFlag. Over a folder handle it
// applies one ReadFlags action to a list of message ids, or to every message in the
// folder when the list is empty ([MS-OXCMSG] 2.2.3.11.1). The flag byte is decoded
// exactly as the single-message ROP (rfClearReadFlag marks unread, otherwise read;
// rfSuppressReceipt suppresses the read receipt; rfGenerateReceiptOnly sends one
// without a state change). PartialCompletion reports whether any message failed.
func (s *Session) ropSetReadFlags(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()              // WantAsynchronous (v1 is always synchronous)
	flags, e2 := p.Uint8()          // ReadFlags
	ids, e3 := p.Uint64ArrayShort() // MessageIds (EID_ARRAY); empty => every message in the folder
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	folder, ok := s.openFolder(out, ropSetReadFlags, handles, hindex, hindex)
	if !ok {
		return true
	}
	// Changing read state modifies the messages: a delegate needs EditAny on the
	// folder, matching the single-message RopSetMessageReadFlag gate.
	if s.denyWrite(out, ropSetReadFlags, hindex, folder.store, folder.folderID, mapi.FrightsEditAny) {
		return true
	}

	action := decodeReadFlags(flags)
	targets, err := readFlagTargets(folder, ids)
	if err != nil {
		writeErr(out, ropSetReadFlags, hindex, ecError)
		return true
	}
	var partial uint8
	for _, mid := range targets {
		if !s.applyReadFlag(folder, mid, action) {
			partial = 1
		}
	}
	out.Uint8(ropSetReadFlags)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
	return true
}

// readFlagAction is what one ReadFlags byte asks for: whether the read state
// changes at all, what it becomes, and whether a read receipt may be sent.
type readFlagAction struct {
	read    bool
	change  bool
	receipt bool
}

// decodeReadFlags maps the ReadFlags byte to the action it selects. It is the
// same exact-value dispatch RopSetMessageReadFlag uses, with the reserved bits
// masked off; an unrecognized value asks for nothing.
func decodeReadFlags(flags uint8) readFlagAction {
	switch flags &^ rfReserved {
	case rfDefault:
		return readFlagAction{read: true, change: true, receipt: true}
	case rfSuppressReceipt:
		return readFlagAction{read: true, change: true} // marked read, receipt suppressed
	case rfClearReadFlag, rfClearReadFlag | rfSuppressReceipt:
		return readFlagAction{change: true} // mark unread; no receipt
	case rfGenerateReceiptOnly:
		return readFlagAction{receipt: true} // receipt only, no state change
	}
	return readFlagAction{}
}

// readFlagTargets resolves the request's message ids. An empty list means every
// message in the folder; an explicit list maps each EID to its store object id.
func readFlagTargets(folder *object, ids []uint64) ([]int64, error) {
	if len(ids) == 0 {
		msgs, err := folder.store.ListMessages(folder.folderID)
		if err != nil {
			return nil, err
		}
		targets := make([]int64, 0, len(msgs))
		for _, m := range msgs {
			targets = append(targets, m.ID)
		}
		return targets, nil
	}
	targets := make([]int64, 0, len(ids))
	for _, eid := range ids {
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		targets = append(targets, int64(mapi.EID(eid).GCValue()))
	}
	return targets, nil
}

// applyReadFlag applies one message's read-state change and any receipt it owes.
// It reports false when the write failed, which makes the whole ROP report
// partial completion.
func (s *Session) applyReadFlag(folder *object, mid int64, action readFlagAction) bool {
	if !action.change {
		if action.receipt {
			s.maybeReadReceipt(folder.store, mid)
		}
		return true
	}
	// A read receipt is owed only on the unread-to-read transition, so read the
	// prior state before writing.
	var wasRead bool
	if action.receipt {
		if w, err := folder.store.GetMessageReadState(mid); err == nil {
			wasRead = w
		}
	}
	if err := folder.store.SetMessageReadState(mid, action.read); err != nil {
		return false
	}
	if action.receipt && action.read && !wasRead {
		s.maybeReadReceipt(folder.store, mid)
	}
	return true
}

func (s *Session) ropDeleteMessages(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	_, e1 := p.Uint8()              // WantAsynchronous (v1 is always synchronous)
	_, e2 := p.Uint8()              // NotifyNonRead (notifications out of scope)
	ids, e3 := p.Uint64ArrayShort() // MessageIds (EID_ARRAY)
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	folder, ok := s.openFolder(out, ropDeleteMessages, handles, hindex, hindex)
	if !ok {
		return true
	}
	// Deleting messages from the folder requires DeleteAny.
	if s.denyWrite(out, ropDeleteMessages, hindex, folder.store, folder.folderID, mapi.FrightsDeleteAny) {
		return true
	}
	var partial uint8
	for _, eid := range ids {
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		if err := folder.store.SoftDeleteObject(int64(mapi.EID(eid).GCValue())); err != nil {
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
	src, dst, ok := s.moveCopyEndpoints(out, handles, hindex, dhindex, wantCopy == 0)
	if !ok {
		return true
	}
	partial := moveCopyEach(src, dst, ids, wantCopy == 0)
	out.Uint8(ropMoveCopyMessages)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
	return true
}

// moveCopyEndpoints resolves the source and destination folder handles and gates
// both sides. ok is false when the response was already written.
//
// A move/copy spans two folders. The copy runs entirely through the source
// store, so source and destination must be the same physical mailbox: a delegate
// session can hold handles into two mailboxes, and the well-known folder ids
// collide across mailboxes, so a cross-mailbox move/copy would file into the
// wrong store. It is refused (an owner never crosses mailboxes, so this is inert
// for an owner). Then the two-sided rights gate: a copy reads the source
// (ReadAny) while a move removes from it (DeleteAny), and either adds to the
// destination (Create). For an owner both authorize checks short-circuit.
func (s *Session) moveCopyEndpoints(out *ext.Push, handles []uint32, hindex, dhindex uint8, move bool) (src, dst *object, ok bool) {
	src, ok = s.openFolder(out, ropMoveCopyMessages, handles, hindex, hindex)
	if !ok {
		return nil, nil, false
	}
	dst = s.get(handleAt(handles, dhindex))
	if dst == nil || dst.kind != kindFolder {
		writeErr(out, ropMoveCopyMessages, hindex, ecError)
		return nil, nil, false
	}
	if dst.store == nil || src.store.Dir() != dst.store.Dir() {
		writeErr(out, ropMoveCopyMessages, hindex, ecNotSupported)
		return nil, nil, false
	}
	srcRight := uint32(mapi.FrightsReadAny)
	if move {
		srcRight = mapi.FrightsDeleteAny // a move deletes from the source
	}
	if s.denyWrite(out, ropMoveCopyMessages, hindex, src.store, src.folderID, srcRight) {
		return nil, nil, false
	}
	if s.denyWrite(out, ropMoveCopyMessages, hindex, dst.store, dst.folderID, mapi.FrightsCreate) {
		return nil, nil, false
	}
	return src, dst, true
}

// moveCopyEach copies every listed message and, for a move, drops the source
// copy afterwards. It returns the PartialCompletion flag: 1 when any id could
// not be processed.
func moveCopyEach(src, dst *object, ids []uint64, move bool) uint8 {
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
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		uid, known := uidByID[int64(mapi.EID(eid).GCValue())]
		if !known || !moveCopyOne(src, dst, uid, move) {
			partial = 1
		}
	}
	return partial
}

// moveCopyOne copies one message and, for a move, deletes the source.
func moveCopyOne(src, dst *object, uid uint32, move bool) bool {
	if err := copyStoredMessage(src.store, src.folderID, uid, dst.folderID); err != nil {
		return false
	}
	if !move {
		return true
	}
	return src.store.DeleteMessage(src.folderID, uid) == nil
}

// copyStoredMessage copies one message from (srcFolder, uid) into dstFolder,
// preserving its flags and received date by re-filing the raw message under a
// fresh uid, the same primitive the webmail move/copy path uses.
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
