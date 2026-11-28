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
// last recipient is not found, a folder or invalid handle is refused, and a
// refusal leaves the stored recipient rows unchanged.
func TestReadRecipientsPastInlineTable(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]

	// Empty message with 0 recipients must answer ecNotFound on row 0.
	emptyResp := dispatchOn(sess, buildReadRecipients(0, 0), msgH)
	wantEC(t, emptyResp, ropReadRecipients, ecNotFound, "ReadRecipients on empty message")

	const total = 300
	rows := make([][]byte, 0, total)
	for i := range total {
		rows = append(rows, buildSMTPRecipientRow(uint32(i), uint8(mapi.RecipTo), fmt.Sprintf("r%d@hermex.test", i), fmt.Sprintf("R%d", i)))
	}
	dispatchOK(t, sess, buildModifyRecipients(0, []mapi.PropTag{mapi.PrSmtpAddress}, rows...), ropModifyRecipients, "ModifyRecipients", msgH)

	resp := dispatchOn(sess, buildReadRecipients(0, 256), msgH)
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

	past := dispatchOn(sess, buildReadRecipients(0, total), msgH)
	wantEC(t, past, ropReadRecipients, ecNotFound, "ReadRecipients past the last row")
	maxID := dispatchOn(sess, buildReadRecipients(0, 0xFFFFFFFF), msgH)
	wantEC(t, maxID, ropReadRecipients, ecNotFound, "ReadRecipients max rowID")

	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)[1]
	viaFolder := dispatchOn(sess, buildReadRecipients(0, 0), folderH)
	wantEC(t, viaFolder, ropReadRecipients, ecNotSupported, "ReadRecipients on a folder")
	badHandle := dispatchOn(sess, buildReadRecipients(0, 0), 0xFFFFFFFF)
	wantEC(t, badHandle, ropReadRecipients, ecError, "ReadRecipients on null handle")

	// Refused reads leave all 300 recipient rows readable afterwards.
	afterRefusal := dispatchOn(sess, buildReadRecipients(0, 299), msgH)
	pAfter := ropOK(t, afterRefusal, ropReadRecipients, "ReadRecipients(299) after refusals")
	wantU8(t, pAfter, "RowCount after refusals", 1)
}
