package rop

import (
	"cmp"
	"slices"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ropReadRecipients is the RopReadRecipients opcode ([MS-OXCROPS] 2.2.6.6).
const ropReadRecipients uint8 = 0x0F

// maxReadRecipientRows bounds one RopReadRecipients answer; the RowCount byte
// could name 255, and a client reads the rest from the next row id.
const maxReadRecipientRows = 0xFE

// ropReadRecipients handles RopReadRecipients ([MS-OXCMSG] 2.2.3.6): it returns an
// open message's recipients from the given row id on, the rows the inline table
// of RopOpenMessage could not hold. Each row carries the same recipient columns
// RopOpenMessage announced. A row id past the last recipient is ecNotFound.
func (s *Session) ropReadRecipients(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	rowID, e1 := p.Uint32() // RowId
	_, e2 := p.Uint16()     // Reserved
	if e1 != nil || e2 != nil {
		return false
	}
	msg := s.get(handleAt(handles, hindex))
	if msg == nil {
		writeErr(out, ropReadRecipients, hindex, ecError)
		return true
	}
	if msg.kind != kindMessage && msg.kind != kindEmbedded && msg.kind != kindNewMessage {
		writeErr(out, ropReadRecipients, hindex, ecNotSupported)
		return true
	}
	recipients, err := messageRecipientBags(msg)
	if err != nil {
		writeErr(out, ropReadRecipients, hindex, notFoundOrError(err))
		return true
	}
	rows := recipientsFrom(recipients, rowID)
	if len(rows) == 0 {
		writeErr(out, ropReadRecipients, hindex, ecNotFound)
		return true
	}
	cols := recipientColumns(recipients)
	out.Uint8(ropReadRecipients)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(uint8(len(rows))) // RowCount
	for _, r := range rows {
		out.Uint32(r.id) // RowId
		writeOpenRecipientRow(out, cols, r.bag)
	}
	return true
}

// numberedRecipient is a recipient with the row id a client addresses it by.
type numberedRecipient struct {
	id  uint32
	bag mapi.PropertyValues
}

// recipientsFrom returns the recipients whose row id is rowID or later, in row
// order, at most maxReadRecipientRows of them. A recipient's row id is the
// PidTagRowid it was saved with, else its position among the message's recipients.
func recipientsFrom(recipients []mapi.PropertyValues, rowID uint32) []numberedRecipient {
	var rows []numberedRecipient
	for i, r := range recipients {
		id := uint32(i) // #nosec G115 -- a recipient index, far below 2^32
		if v, ok := r.Get(mapi.PrRowid); ok {
			if n, ok := v.(int32); ok {
				id = uint32(n) // #nosec G115 -- the unsigned view of the stored 32 bits
			}
		}
		if id >= rowID {
			rows = append(rows, numberedRecipient{id: id, bag: r})
		}
	}
	slices.SortStableFunc(rows, func(a, b numberedRecipient) int { return cmp.Compare(a.id, b.id) })
	return rows[:min(len(rows), maxReadRecipientRows)]
}
