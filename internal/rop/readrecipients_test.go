package rop

import (
	"fmt"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildReadRecipients builds a RopReadRecipients request (RowId, Reserved).
func buildReadRecipients(inIdx uint8, rowID uint32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropReadRecipients)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint32(rowID)
	b.Uint16(0) // Reserved
	return b.Bytes()
}

// TestReadRecipientsPastInlineTable proves a client reaches the recipients the
// inline table of RopOpenMessage cannot hold: a message with 300 recipients
// answers row 256 onward with each row's own id and address, a row id past the
// last recipient is not found, and a folder handle is refused.
func TestReadRecipientsPastInlineTable(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	const total = 300
	rows := make([][]byte, 0, total)
	for i := range total {
		rows = append(rows, buildSMTPRecipientRow(uint32(i), uint8(mapi.RecipTo), fmt.Sprintf("r%d@hermex.test", i), fmt.Sprintf("R%d", i)))
	}
	mr, _ := sess.Dispatch(buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, rows...), []uint32{msgH})
	ropOK(t, mr, ropModifyRecipients, "ModifyRecipients")

	resp, _ := sess.Dispatch(buildReadRecipients(0, 256), []uint32{msgH})
	p := ropOK(t, resp, ropReadRecipients, "ReadRecipients(256)")
	wantU8(t, p, "RowCount", total-256)
	cols := []mapi.PropTag{mapi.PrSmtpAddress}
	for i := 256; i < total; i++ {
		label := fmt.Sprintf("row %d", i)
		wantU32(t, p, label+" RowId", uint32(i))
		wantU8(t, p, label+" type", uint8(mapi.RecipTo))
		bag := pullOpenRecipientRest(t, p, cols, label)
		wantProp(t, bag, mapi.PrEmailAddress, fmt.Sprintf("r%d@hermex.test", i), label+" email")
	}
	if p.Remaining() != 0 {
		t.Errorf("%d bytes left after the last row", p.Remaining())
	}

	past, _ := sess.Dispatch(buildReadRecipients(0, total), []uint32{msgH})
	wantEC(t, past, ropReadRecipients, ecNotFound, "ReadRecipients past the last row")
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	viaFolder, _ := sess.Dispatch(buildReadRecipients(0, 0), []uint32{h[1]})
	wantEC(t, viaFolder, ropReadRecipients, ecNotSupported, "ReadRecipients on a folder")
}
