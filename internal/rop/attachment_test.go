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
	defer st.Close()
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

// TestAttachmentReadChain walks the full attachment read path: open the message,
// open its attachment table, QueryRows the attachment (PR_ATTACH_NUM +
// filename), OpenAttachment by that number, then OpenStream + ReadStream its
// data.
func TestAttachmentReadChain(t *testing.T) {
	dir := t.TempDir()
	msgID := seedAttachmentMessage(t, dir)
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir)
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// GetAttachmentTable: bare success header, no row count.
	gat, h := sess.Dispatch(buildGetAttachmentTable(0, 1), []uint32{msgH, 0xFFFFFFFF})
	p := ext.NewPull(gat, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetAttachmentTable {
		t.Fatalf("GetAttachmentTable RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetAttachmentTable ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Errorf("GetAttachmentTable response has %d trailing bytes, want a bare header", p.Remaining())
	}
	tableH := h[1]

	// QueryRows the attachment table for PR_ATTACH_NUM + filename.
	cols := []mapi.PropTag{mapi.PrAttachNum, mapi.PrAttachLongFilename}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("attachment table rows = %d, want 1", len(rows))
	}
	if num, _ := rows[0].Get(mapi.PrAttachNum); num != int32(0) {
		t.Errorf("PR_ATTACH_NUM = %v, want 0", num)
	}
	if fn, _ := rows[0].Get(mapi.PrAttachLongFilename); fn != "a.bin" {
		t.Errorf("attachment filename = %v, want \"a.bin\"", fn)
	}

	// OpenAttachment(num=0): bare success header.
	oa, h := sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{msgH, 0xFFFFFFFF})
	p = ext.NewPull(oa, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropOpenAttachment {
		t.Fatalf("OpenAttachment RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenAttachment ReturnValue = %#x", ec)
	}
	attachH := h[1]

	// OpenStream + ReadStream the attachment data.
	os, h := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), 0), []uint32{attachH, 0xFFFFFFFF})
	p = ext.NewPull(os, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenStream(attachment) ReturnValue = %#x", ec)
	}
	if size := mustU32(t, p, "StreamSize"); size != uint32(len("ATTACHDATA")) {
		t.Errorf("attachment StreamSize = %d, want %d", size, len("ATTACHDATA"))
	}
	streamH := h[1]
	if got := readStreamChunk(t, sess, streamH, 0xFFFF, 0); !bytes.Equal(got, []byte("ATTACHDATA")) {
		t.Errorf("attachment data = %q, want \"ATTACHDATA\"", got)
	}
}
