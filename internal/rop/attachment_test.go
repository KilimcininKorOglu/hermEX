package rop

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildGetAttachmentTable builds a RopGetAttachmentTable request.
func buildGetAttachmentTable(inIdx, outIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetAttachmentTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(0) // TableFlags
	return b.Bytes()
}

// buildOpenAttachment builds a RopOpenAttachment request (OutputHandleIndex,
// Flags, AttachmentId).
func buildOpenAttachment(inIdx, outIdx uint8, attachID uint32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropOpenAttachment)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(0) // OpenAttachmentFlags
	b.Uint32(attachID)
	return b.Bytes()
}

// seedAttachmentMessage delivers a multipart/mixed message with one
// base64-encoded attachment ("a.bin" carrying "ATTACHDATA") and returns the
// message's objectstore id.
func seedAttachmentMessage(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	data := base64.StdEncoding.EncodeToString([]byte("ATTACHDATA"))
	raw := "From: sender@hermex.test\r\nTo: alice@hermex.test\r\nSubject: ATTACHMSG\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nbody text\r\n" +
		"--B\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"a.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + data + "\r\n--B--\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// seedTwoAttachmentMessage delivers a message with two attachments into folder
// and returns the stored message.
func seedTwoAttachmentMessage(t *testing.T, st *objectstore.Store, folder int64) objectstore.MessageInfo {
	t.Helper()
	part := func(name string) string {
		return "--B\r\nContent-Type: application/octet-stream\r\n" +
			"Content-Disposition: attachment; filename=\"" + name + "\"\r\n\r\n" + name + "\r\n"
	}
	raw := "From: sender@hermex.test\r\nTo: alice@hermex.test\r\nSubject: TWOATTACH\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nbody\r\n" + part("a.bin") + part("b.bin") + "--B--\r\n"
	info, err := st.AppendMessage(folder, []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// attachmentKeyRows opens a message's attachment table and reads its PidTagMid
// and PidTagRecordKey columns, PidTagMid first as a MAPI client asks for it.
func attachmentKeyRows(t *testing.T, sess *Session, folderFID, msgID int64) []mapi.PropertyValues {
	t.Helper()
	tableH := openAttachmentTable(t, sess, folderFID, msgID)
	cols := []mapi.PropTag{mapi.PrMid, mapi.PrRecordKey}
	dispatchOK(t, sess, buildSetColumns(0, cols), ropSetColumns, "SetColumns", tableH)
	_, rows := queryRowsResponse(t, dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH), cols)
	return rows
}

// openAttachmentTable logs on, opens message msgID in folderFID and returns the
// handle of its attachment table, failing the test on any refused step.
func openAttachmentTable(t *testing.T, sess *Session, folderFID, msgID int64) uint32 {
	t.Helper()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	folderEID := uint64(mapi.MakeEIDEx(1, uint64(folderFID)))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	h = dispatchOK(t, sess, buildOpenMessage(0, 1, folderEID, msgEID), ropOpenMessage, "OpenMessage", h[0], 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildGetAttachmentTable(0, 1), ropGetAttachmentTable, "GetAttachmentTable", h[1], 0xFFFFFFFF)
	return h[1]
}

// rowProp returns tag from row, failing the test when the row lacks it.
func rowProp(t *testing.T, row mapi.PropertyValues, tag mapi.PropTag, label string) any {
	t.Helper()
	v, ok := row.Get(tag)
	if !ok {
		t.Fatalf("%s: property %#x missing from the row", label, uint32(tag))
	}
	return v
}

// TestAttachmentTableRowKeys proves each attachment row carries its own
// PidTagMid, so a client building PidTagInstanceKey from the first column gets a
// distinct key per row, and a PidTagRecordKey of the lead byte and the
// attachment's position that stays the same when the message moves.
func TestAttachmentTableRowKeys(t *testing.T) {
	dir := provisionedDir(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	stored := seedTwoAttachmentMessage(t, st, inbox)
	wantKeys := [][]byte{
		{0xA2, 0, 0, 0, 0, 0, 0, 0, 0},
		{0xA2, 0, 0, 0, 0, 0, 0, 0, 1},
	}
	check := func(label string, folder, id int64) {
		sess := NewSession(dir, nil, "")
		defer sess.Close()
		rows := attachmentKeyRows(t, sess, folder, id)
		if len(rows) != 2 {
			t.Fatalf("%s: attachment rows = %d, want 2", label, len(rows))
		}
		for i, row := range rows {
			if got := rowProp(t, row, mapi.PrRecordKey, label); !bytes.Equal(asBytes(got), wantKeys[i]) {
				t.Errorf("%s: row %d PidTagRecordKey = %x, want %x", label, i, got, wantKeys[i])
			}
		}
		mid0 := rowProp(t, rows[0], mapi.PrMid, label)
		mid1 := rowProp(t, rows[1], mapi.PrMid, label)
		if mid0 == mid1 {
			t.Errorf("%s: PidTagMid per row = %v, %v, want two distinct values", label, mid0, mid1)
		}
	}
	check("before the move", inbox, stored.ID)

	moved, err := st.MoveMessage(inbox, stored.UID, int64(mapi.PrivateFIDDeletedItems))
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	check("after the move", int64(mapi.PrivateFIDDeletedItems), moved.ID)
}

// TestAttachmentReadChain walks the full attachment read path: open the message,
// open its attachment table, QueryRows the attachment (PR_ATTACH_NUM +
// filename), OpenAttachment by that number, then OpenStream + ReadStream its
// data.
func TestAttachmentReadChain(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedAttachmentMessage(t, dir)
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)
	msgH := h[1]

	// GetAttachmentTable: bare success header, no row count.
	gat, h := sess.Dispatch(buildGetAttachmentTable(0, 1), []uint32{msgH, 0xFFFFFFFF})
	p := ropOK(t, gat, ropGetAttachmentTable, "GetAttachmentTable")
	wantDrained(t, p, "GetAttachmentTable (its response is a bare header)")
	tableH := h[1]

	// QueryRows the attachment table for PR_ATTACH_NUM + filename.
	cols := []mapi.PropTag{mapi.PrAttachNum, mapi.PrAttachLongFilename}
	dispatchOK(t, sess, buildSetColumns(0, cols), ropSetColumns, "SetColumns", tableH)
	_, rows := queryRowsResponse(t, dispatchOn(sess, buildQueryRows(0, 0, 1, 32), tableH), cols)
	if len(rows) != 1 {
		t.Fatalf("attachment table rows = %d, want 1", len(rows))
	}
	wantProp(t, rows[0], mapi.PrAttachNum, int32(0), "PR_ATTACH_NUM")
	wantProp(t, rows[0], mapi.PrAttachLongFilename, "a.bin", "attachment filename")

	// OpenAttachment(num=0): bare success header.
	oa, h := sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	ropOK(t, oa, ropOpenAttachment, "OpenAttachment")
	attachH := h[1]

	// OpenStream + ReadStream the attachment data.
	os, h := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), 0), []uint32{attachH, 0xFFFFFFFF})
	p = ropOK(t, os, ropOpenStream, "OpenStream(attachment)")
	wantU32(t, p, "attachment StreamSize", uint32(len("ATTACHDATA")))
	streamH := h[1]
	if got := readStreamChunk(t, sess, streamH, 0xFFFF, 0); !bytes.Equal(got, []byte("ATTACHDATA")) {
		t.Errorf("attachment data = %q, want \"ATTACHDATA\"", got)
	}
}

// TestAttachmentTableColumnsAll proves RopQueryColumnsAll on an attachment table
// lists the attachments' stored tags and the synthesized row keys, not only the
// chosen columns.
func TestAttachmentTableColumnsAll(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	tableH := openAttachmentTable(t, sess, int64(mapi.PrivateFIDInbox), msgID)
	dispatchOK(t, sess, buildSetColumns(0, []mapi.PropTag{mapi.PrAttachNum}), ropSetColumns, "SetColumns", tableH)
	assertColumnsAll(t, sess, tableH, mapi.PrAttachNum, mapi.PrAttachLongFilename, mapi.PrRecordKey, mapi.PrMid)
}

// TestOpenAttachmentRefusesUnknownNumber proves RopOpenAttachment answers
// ecNotFound ([MS-OXCMSG] 2.2.3.12, an AttachmentId the message does not hold)
// for a number past the message's only attachment, opens no object, and leaves
// the message's one attachment readable.
func TestOpenAttachmentRefusesUnknownNumber(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedAttachmentMessage(t, dir)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	h = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", h[0], 0xFFFFFFFF)
	msgH := h[1]

	for _, bogus := range []uint32{1, 0xFFFFFFFF} {
		resp, out := sess.Dispatch(buildOpenAttachment(0, 1, bogus), []uint32{msgH, 0xFFFFFFFF})
		wantEC(t, resp, ropOpenAttachment, ecNotFound, "OpenAttachment of an unknown number")
		if out[1] != 0xFFFFFFFF && sess.get(out[1]) != nil {
			t.Errorf("OpenAttachment(%#x) registered handle %d, want none", bogus, out[1])
		}
	}
	dispatchOK(t, sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment after the refusals", msgH, 0xFFFFFFFF)
}
