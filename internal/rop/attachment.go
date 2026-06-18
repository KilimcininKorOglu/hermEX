package rop

import (
	"errors"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// messageAttachmentBags reads a message's attachments as their property bags.
// objectstore exposes attachments only through the re-synthesized message, so
// the bags are taken from there; PR_ATTACH_NUM (the row index) is synthesized
// by the table/open layer, not stored.
func messageAttachmentBags(o *object) ([]mapi.PropertyValues, error) {
	msg, err := o.store.OpenMessage(o.messageID)
	if err != nil {
		return nil, err
	}
	bags := make([]mapi.PropertyValues, len(msg.Attachments))
	for i := range msg.Attachments {
		bags[i] = msg.Attachments[i].Props
	}
	return bags, nil
}

// resolveAttachment finds the attachment bag a client's AttachmentId addresses.
// AttachmentId is the stored PidTagAttachNumber, which is stable across sibling
// deletes; the match is therefore by that property, not by row position. When no
// bag carries a stored number (legacy data predating stored attach numbers),
// AttachmentId is treated as the row ordinal — the same fallback the attachment
// table's column synthesis uses, so the two read paths agree.
func resolveAttachment(bags []mapi.PropertyValues, attachID uint32) (mapi.PropertyValues, bool) {
	anyNumbered := false
	for _, b := range bags {
		if v, ok := b.Get(mapi.PrAttachNum); ok {
			anyNumbered = true
			if n, ok := v.(int32); ok && uint32(n) == attachID {
				return b, true
			}
		}
	}
	if !anyNumbered && int(attachID) < len(bags) {
		return bags[attachID], true
	}
	return nil, false
}

// ropGetAttachmentTable handles RopGetAttachmentTable ([MS-OXCMSG] 2.2.3.18): it
// snapshots the message's attachments into a new attachment table. The response
// is the bare header — the client reads the rows with QueryRows (the row count
// is not in the open response, unlike the contents/hierarchy tables).
func (s *Session) ropGetAttachmentTable(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	_, e2 := p.Uint8()       // TableFlags
	if e1 != nil || e2 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil || msg.kind != kindMessage || msg.store == nil {
		writeErr(out, ropGetAttachmentTable, ohindex, ecError)
		return true
	}
	bags, err := messageAttachmentBags(msg)
	if err != nil {
		writeErr(out, ropGetAttachmentTable, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{
		kind:  kindTable,
		store: msg.store,
		table: &tableState{kind: tableAttachment, attachments: bags},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropGetAttachmentTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropOpenAttachment handles RopOpenAttachment ([MS-OXCMSG] 2.2.3.20): it opens
// the attachment whose PR_ATTACH_NUM (the index the attachment table reports)
// matches AttachmentId, registering an attachment object over its property bag.
// The response is the bare header; the attachment's data is read via OpenStream
// on PrAttachDataBin.
func (s *Session) ropOpenAttachment(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()   // OutputHandleIndex
	_, e2 := p.Uint8()         // OpenAttachmentFlags
	attachID, e3 := p.Uint32() // AttachmentId (= PR_ATTACH_NUM)
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil || msg.kind != kindMessage || msg.store == nil {
		writeErr(out, ropOpenAttachment, ohindex, ecError)
		return true
	}
	bags, err := messageAttachmentBags(msg)
	if err != nil {
		writeErr(out, ropOpenAttachment, ohindex, ecNotFound)
		return true
	}
	bag, ok := resolveAttachment(bags, attachID)
	if !ok {
		writeErr(out, ropOpenAttachment, ohindex, ecNotFound)
		return true
	}
	h := s.alloc(&object{kind: kindAttachment, store: msg.store, attachProps: bag})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenAttachment)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// ropCreateAttachment handles RopCreateAttachment ([MS-OXCMSG] 2.2.3.6): it
// creates a new attachment on the open message and returns its assigned attach
// number. The attachment row is inserted up front (with the opening properties
// the reference stamps on a new attachment) so the number can be assigned and
// returned now; the client then fills the payload via SetProperties and persists
// it with SaveChangesAttachment. The input handle is the parent message; the
// output handle receives the attachment object. v1 supports this only on an
// opened (persisted) message — a not-yet-saved composed message has no store row
// to attach to.
func (s *Session) ropCreateAttachment(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	if e1 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil || msg.kind != kindMessage || msg.store == nil {
		writeErr(out, ropCreateAttachment, ohindex, ecNotSupported)
		return true
	}
	now := mapi.UnixToNTTime(time.Now())
	initial := mapi.PropertyValues{
		{Tag: mapi.PrRenderingPosition, Value: int32(-1)}, // 0xFFFFFFFF: not rendered in the body
		{Tag: mapi.PrCreationTime, Value: now},
		{Tag: mapi.PrLastModificationTime, Value: now},
	}
	aid, num, err := msg.store.CreateAttachment(msg.messageID, initial)
	if err != nil {
		writeErr(out, ropCreateAttachment, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{
		kind:    kindAttachWrite,
		store:   msg.store,
		attachW: &attachWrite{messageID: msg.messageID, attachmentID: aid, attachNum: num},
	})
	setHandle(handles, ohindex, h)

	out.Uint8(ropCreateAttachment)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(num) // AttachmentID (= PidTagAttachNumber)
	return true
}

// ropSaveChangesAttachment handles RopSaveChangesAttachment ([MS-OXCMSG] 2.2.3.8):
// it flushes the attachment's buffered properties to its stored row. The handle
// wiring is asymmetric with CreateAttachment and load-bearing: the common-header
// handle resolves the parent MESSAGE, while the body's InputHandleIndex (ihindex2)
// resolves the ATTACHMENT being saved. The save marks the parent message dirty so
// its own SaveChangesMessage advances the change number — an attachment change is
// observed by ICS only through the message's change number, which this ROP does
// not itself bump.
func (s *Session) ropSaveChangesAttachment(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ihindex2, e1 := p.Uint8() // InputHandleIndex (indexes the attachment object)
	_, e2 := p.Uint8()        // SaveFlags
	if e1 != nil || e2 != nil {
		return false
	}
	att := s.get(handleAt(handles, ihindex2))
	if att == nil || att.kind != kindAttachWrite || att.attachW == nil || att.store == nil {
		writeErr(out, ropSaveChangesAttachment, hindex, ecError)
		return true
	}
	if len(att.attachW.pending) > 0 {
		if err := att.store.SetAttachmentProperties(att.attachW.attachmentID, att.attachW.pending); err != nil {
			writeErr(out, ropSaveChangesAttachment, hindex, ecError)
			return true
		}
		att.attachW.pending = nil
	}
	// Mark the parent message (the common-header handle) dirty so SaveChangesMessage
	// bumps its change number even when no top-level property changed.
	if msg := s.get(handleAt(handles, hindex)); msg != nil && msg.kind == kindMessage {
		msg.touched = true
	}

	out.Uint8(ropSaveChangesAttachment)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}

// ropDeleteAttachment handles RopDeleteAttachment ([MS-OXCMSG] 2.2.3.7): it
// deletes the attachment the message holds at AttachmentId (its stored attach
// number), reporting MAPI_E_NOT_FOUND when none exists there. The input handle is
// the parent message; the response is the bare header. It marks the message dirty
// so a following SaveChangesMessage advances the change number.
func (s *Session) ropDeleteAttachment(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	attachID, e1 := p.Uint32() // AttachmentId (= PidTagAttachNumber)
	if e1 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil || msg.kind != kindMessage || msg.store == nil {
		writeErr(out, ropDeleteAttachment, hindex, ecError)
		return true
	}
	if err := msg.store.DeleteAttachment(msg.messageID, attachID); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			writeErr(out, ropDeleteAttachment, hindex, ecNotFound)
			return true
		}
		writeErr(out, ropDeleteAttachment, hindex, ecError)
		return true
	}
	msg.touched = true

	out.Uint8(ropDeleteAttachment)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}
