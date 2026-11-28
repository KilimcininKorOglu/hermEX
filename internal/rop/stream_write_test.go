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
	mustBuild(b.BinShort(data))
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
	ws := dispatchOn(sess, buildWriteStream(0, data), streamH)
	p := ropOK(t, ws, ropWriteStream, "WriteStream")
	return mustU16(t, p, "WrittenSize")
}

// TestStreamWriteAttachmentData drives the large-attachment-payload write path.
func TestStreamWriteAttachmentData(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "STREAMHOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	os, h := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), mapiModify), []uint32{attH, 0xFFFFFFFF})
	p := ropOK(t, os, ropOpenStream, "OpenStream(write)")
	wantU32(t, p, "new attachment stream size", 0)
	streamH := h[1]

	if n := writeStreamWritten(t, sess, streamH, []byte("HELLO ")); n != 6 {
		t.Fatalf("first WriteStream WrittenSize = %d, want 6", n)
	}
	if n := writeStreamWritten(t, sess, streamH, []byte("WORLD")); n != 5 {
		t.Fatalf("second WriteStream WrittenSize = %d, want 5", n)
	}

	gs := dispatchOn(sess, buildGetStreamSize(0), streamH)
	p = ropOK(t, gs, ropGetStreamSize, "GetStreamSize")
	wantU32(t, p, "stream size after writes", 11)

	sk := dispatchOn(sess, buildSeekStream(0, streamSeekSet, 0), streamH)
	p = ropOK(t, sk, ropSeekStream, "SeekStream(SET,0)")
	wantU64(t, p, "SeekStream(SET,0) NewPosition", 0)
	if got := readStreamChunk(t, sess, streamH, 64, 0); !bytes.Equal(got, []byte("HELLO WORLD")) {
		t.Errorf("read-after-write = %q, want HELLO WORLD", got)
	}

	dispatchOK(t, sess, buildCommitStream(0), ropCommitStream, "CommitStream", streamH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)
	saveChangesEID(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH))

	assertAttachmentCount(t, store, mid, 1, "host message")
	saved, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	v := rowProp(t, saved.Attachments[0].Props, mapi.PrAttachDataBin, "stored attachment data")
	vb, ok := v.([]byte)
	if !ok {
		t.Fatalf("stored attachment data = %T, want []byte", v)
	}
	if !bytes.Equal(vb, []byte("HELLO WORLD")) {
		t.Errorf("stored attachment data = %q, want HELLO WORLD", vb)
	}
}

// TestStreamWriteMechanics covers stream-resize and access-control mechanics.
func TestStreamWriteMechanics(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "MECHHOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	// Read-only stream refuses writes, resizes, and commits while preserving size.
	roH := dispatchOK(t, sess, buildOpenStream(0, 1, uint32(mapi.PrBody), 0), ropOpenStream, "OpenStream(ro)", msgH, 0xFFFFFFFF)[1]
	origSize := mustU32(t, ropOK(t, dispatchOn(sess, buildGetStreamSize(0), roH), ropGetStreamSize, "GetStreamSize(ro init)"), "origSize")
	wantEC(t, dispatchOn(sess, buildWriteStream(0, []byte("X")), roH), ropWriteStream, ecAccessDenied, "WriteStream on read-only stream")
	wantEC(t, dispatchOn(sess, buildSetStreamSize(0, 1), roH), ropSetStreamSize, ecAccessDenied, "SetStreamSize on read-only stream")
	wantEC(t, dispatchOn(sess, buildCommitStream(0), 0xFFFFFFFF), ropCommitStream, ecError, "CommitStream on null handle")
	dispatchOK(t, sess, buildCommitStream(0), ropCommitStream, "CommitStream on read-only stream (no-op)", roH)
	wantU32(t, ropOK(t, dispatchOn(sess, buildGetStreamSize(0), roH), ropGetStreamSize, "GetStreamSize(ro after refusals)"), "size after refusals", origSize)

	// Writable stream: grow to 4, then write "AB" at start.
	streamH := dispatchOK(t, sess, buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), mapiModify), ropOpenStream, "OpenStream(rw)", attH, 0xFFFFFFFF)[1]
	dispatchOK(t, sess, buildSetStreamSize(0, 4), ropSetStreamSize, "SetStreamSize(grow)", streamH)
	dispatchOK(t, sess, buildSeekStream(0, streamSeekSet, 0), ropSeekStream, "SeekStream(SET,0)", streamH)
	if n := writeStreamWritten(t, sess, streamH, []byte("AB")); n != 2 {
		t.Fatalf("WriteStream(AB) = %d, want 2", n)
	}
	dispatchOK(t, sess, buildSeekStream(0, streamSeekSet, 0), ropSeekStream, "SeekStream(SET,0)", streamH)
	if got := readStreamChunk(t, sess, streamH, 64, 0); !bytes.Equal(got, []byte("AB\x00\x00")) {
		t.Errorf("after grow+write = %q, want AB\\x00\\x00", got)
	}

	dispatchOK(t, sess, buildSetStreamSize(0, 1), ropSetStreamSize, "SetStreamSize(shrink)", streamH)
	dispatchOK(t, sess, buildSeekStream(0, streamSeekSet, 0), ropSeekStream, "SeekStream(SET,0)", streamH)
	if got := readStreamChunk(t, sess, streamH, 64, 0); !bytes.Equal(got, []byte("A")) {
		t.Errorf("after shrink = %q, want A", got)
	}

	se := dispatchOn(sess, buildSeekStream(0, streamSeekEnd, 0), streamH)
	p := ropOK(t, se, ropSeekStream, "SeekStream(END,0)")
	if pos := mustU64(t, p, "NewPosition"); pos != 1 {
		t.Errorf("SeekStream(END,0) NewPosition = %d, want 1", pos)
	}
}

// TestStreamReadComposeAttachment tests read-your-writes on compose attachments.
func TestStreamReadComposeAttachment(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	msgH := dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]
	_, attH := createAttachmentNum(t, sess, msgH)
	want := []byte("compose-attach-bytes")
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrAttachDataBin, Value: want}}), ropSetProperties, "SetProperties(compose attach)", attH)

	if got := openReadStreamAll(t, sess, attH, uint32(mapi.PrAttachDataBin)); !bytes.Equal(got, want) {
		t.Errorf("read-mode stream over compose attachment = %q, want %q", got, want)
	}
}

// TestStreamReadPersistedAttachmentAfterSave tests post-save fallback to store rows.
func TestStreamReadPersistedAttachmentAfterSave(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := seedInboxMessage(t, dir, "PERSATT")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	_, attH := createAttachmentNum(t, sess, msgH)
	want := []byte("persisted-attach")
	dispatchOK(t, sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrAttachDataBin, Value: want}}), ropSetProperties, "SetProperties(attach)", attH)
	dispatchOK(t, sess, buildSaveChangesAttachment(0, 1), ropSaveChangesAttachment, "SaveChangesAttachment", msgH, attH)

	if got := openReadStreamAll(t, sess, attH, uint32(mapi.PrAttachDataBin)); !bytes.Equal(got, want) {
		t.Errorf("post-save read over persisted attachment write handle = %q, want %q", got, want)
	}
}

// TestStreamSizeCapped proves RopSetStreamSize and RopSeekStream refuse oversized positions.
func TestStreamSizeCapped(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "CAPHOST"))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	_, attH := createAttachmentNum(t, sess, msgH)
	streamH := dispatchOK(t, sess, buildOpenStream(0, 1, uint32(mapi.PrAttachDataBin), mapiModify), ropOpenStream, "OpenStream(cap)", attH, 0xFFFFFFFF)[1]

	for _, size := range []uint64{maxStreamSize + 1, 1 << 40, 1 << 63} {
		wantEC(t, dispatchOn(sess, buildSetStreamSize(0, size), streamH), ropSetStreamSize, ecInvalidParam, "SetStreamSize(over cap)")
	}

	wantEC(t, dispatchOn(sess, buildSeekStream(0, streamSeekSet, 1<<40), streamH), ropSeekStream, ecInvalidParam, "SeekStream(SET, 1<<40)")

	gs := dispatchOn(sess, buildGetStreamSize(0), streamH)
	wantU32(t, ropOK(t, gs, ropGetStreamSize, "GetStreamSize"), "stream size after refused resizes", 0)
}
