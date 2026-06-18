package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
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
