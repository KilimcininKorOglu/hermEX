package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildWriteStream builds a RopWriteStream request (a u16-prefixed short binary).
func buildWriteStream(inIdx uint8, data []byte) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropWriteStream)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	_ = b.BinShort(data)
	return b.Bytes()
}

// buildCommitStream builds a RopCommitStream request (header only).
func buildCommitStream(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCommitStream)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	return b.Bytes()
}

// buildSeekStream builds a RopSeekStream request (Origin u8, Offset u64).
func buildSeekStream(inIdx, origin uint8, offset uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSeekStream)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(origin)
	b.Uint64(offset)
	return b.Bytes()
}

// buildSetStreamSize builds a RopSetStreamSize request (StreamSize u64).
func buildSetStreamSize(inIdx uint8, size uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetStreamSize)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint64(size)
	return b.Bytes()
}

// buildGetStreamSize builds a RopGetStreamSize request (header only).
func buildGetStreamSize(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetStreamSize)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	return b.Bytes()
}

// writeStreamWritten dispatches one RopWriteStream and returns the WrittenSize.
func writeStreamWritten(t *testing.T, sess *Session, streamH uint32, data []byte) uint16 {
	t.Helper()
	ws, _ := sess.Dispatch(buildWriteStream(0, data), []uint32{streamH})
	p := ext.NewPull(ws, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropWriteStream {
		t.Fatalf("WriteStream RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("WriteStream ReturnValue = %#x", ec)
	}
	return mustU16(t, p, "WrittenSize")
}

// TestStreamWriteAttachmentData drives the large-attachment-payload write path: a
// stream is opened for write over a created attachment's PR_ATTACH_DATA_BIN, the
// payload is written in two chunks, read back on the same stream after a seek, then
// committed and saved. It proves the written bytes reach the stored attachment —
// the path SetProperties was too small for and CreateAttachment deferred to streams.
func TestStreamWriteAttachmentData(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "STREAMHOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	// OpenStream for write (ReadWrite mode) over the attachment's data property.
	os, h := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), mapiModify), []uint32{attH, 0xFFFFFFFF})
	p := ext.NewPull(os, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenStream(write) ReturnValue = %#x", ec)
	}
	if sz := mustU32(t, p, "StreamSize"); sz != 0 {
		t.Fatalf("new attachment stream size = %d, want 0", sz)
	}
	streamH := h[1]

	// Write the payload in two chunks; the cursor advances across them.
	if n := writeStreamWritten(t, sess, streamH, []byte("HELLO ")); n != 6 {
		t.Fatalf("first WriteStream WrittenSize = %d, want 6", n)
	}
	if n := writeStreamWritten(t, sess, streamH, []byte("WORLD")); n != 5 {
		t.Fatalf("second WriteStream WrittenSize = %d, want 5", n)
	}

	// GetStreamSize reflects both writes.
	gs, _ := sess.Dispatch(buildGetStreamSize(0), []uint32{streamH})
	p = ext.NewPull(gs, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	mustU32(t, p, "ec")
	if sz := mustU32(t, p, "StreamSize"); sz != 11 {
		t.Fatalf("stream size after writes = %d, want 11", sz)
	}

	// Seek to the start and read the written bytes back on the same stream.
	sk, _ := sess.Dispatch(buildSeekStream(0, streamSeekSet, 0), []uint32{streamH})
	p = ext.NewPull(sk, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	mustU32(t, p, "ec")
	if pos, _ := p.Uint64(); pos != 0 {
		t.Fatalf("SeekStream(SET,0) NewPosition = %d, want 0", pos)
	}
	if got := readStreamChunk(t, sess, streamH, 64, 0); !bytes.Equal(got, []byte("HELLO WORLD")) {
		t.Errorf("read-after-write = %q, want HELLO WORLD", got)
	}

	// Commit stages the bytes into the attachment; SaveChangesAttachment + the
	// carrier save persist them.
	cm, _ := sess.Dispatch(buildCommitStream(0), []uint32{streamH})
	p = ext.NewPull(cm, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CommitStream ReturnValue = %#x", ec)
	}
	scA, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	pa := ext.NewPull(scA, ext.FlagUTF16)
	mustU8(t, pa, "RopId")
	mustU8(t, pa, "hindex")
	if ec := mustU32(t, pa, "ec"); ec != ecSuccess {
		t.Fatalf("SaveChangesAttachment ReturnValue = %#x", ec)
	}
	sc, _ := sess.Dispatch(buildSaveChangesMessage(0, 1), []uint32{logonH, msgH})
	saveChangesEID(t, sc)

	// White-box: the streamed payload is the stored attachment's data.
	saved, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Attachments) != 1 {
		t.Fatalf("host message has %d attachments, want 1", len(saved.Attachments))
	}
	v, _ := saved.Attachments[0].Props.Get(mapi.PrAttachDataBin)
	if vb, _ := v.([]byte); !bytes.Equal(vb, []byte("HELLO WORLD")) {
		t.Errorf("stored attachment data = %q, want HELLO WORLD", vb)
	}
}

// TestStreamWriteMechanics covers the stream-resize and access-control mechanics: a
// read-only stream refuses a write, SetStreamSize grows with zero-fill and shrinks
// with truncation, and SeekStream(END) positions at the end for an append.
func TestStreamWriteMechanics(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "MECHHOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	// A read-only stream (open mode 0, over the readable message body) refuses writes.
	_, h = sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrBody), 0), []uint32{msgH, 0xFFFFFFFF})
	roH := h[1]
	ws, _ := sess.Dispatch(buildWriteStream(0, []byte("X")), []uint32{roH})
	p := ext.NewPull(ws, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecAccessDenied {
		t.Errorf("WriteStream on a read-only stream ec = %#x, want ecAccessDenied", ec)
	}

	// A writable stream: grow to 4 (zero-filled), then write two bytes at the start.
	_, h = sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), mapiModify), []uint32{attH, 0xFFFFFFFF})
	streamH := h[1]
	ss, _ := sess.Dispatch(buildSetStreamSize(0, 4), []uint32{streamH})
	p = ext.NewPull(ss, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SetStreamSize(grow) ReturnValue = %#x", ec)
	}
	// SeekStream(SET,0) then write "AB" → buffer is "AB\x00\x00".
	sess.Dispatch(buildSeekStream(0, streamSeekSet, 0), []uint32{streamH})
	writeStreamWritten(t, sess, streamH, []byte("AB"))
	sess.Dispatch(buildSeekStream(0, streamSeekSet, 0), []uint32{streamH})
	if got := readStreamChunk(t, sess, streamH, 64, 0); !bytes.Equal(got, []byte("AB\x00\x00")) {
		t.Errorf("after grow+write = %q, want AB\\x00\\x00", got)
	}

	// Shrink to 1 byte: truncates to "A".
	sess.Dispatch(buildSetStreamSize(0, 1), []uint32{streamH})
	sess.Dispatch(buildSeekStream(0, streamSeekSet, 0), []uint32{streamH})
	if got := readStreamChunk(t, sess, streamH, 64, 0); !bytes.Equal(got, []byte("A")) {
		t.Errorf("after shrink = %q, want A", got)
	}

	// SeekStream(END,0) positions at the (now 1-byte) end for an append.
	se, _ := sess.Dispatch(buildSeekStream(0, streamSeekEnd, 0), []uint32{streamH})
	p = ext.NewPull(se, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	mustU32(t, p, "ec")
	if pos, _ := p.Uint64(); pos != 1 {
		t.Errorf("SeekStream(END,0) NewPosition = %d, want 1", pos)
	}
}
