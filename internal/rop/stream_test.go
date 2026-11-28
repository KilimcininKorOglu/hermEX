package rop

import (
	"strings"
	"testing"
	"unicode/utf16"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildOpenStream builds a RopOpenStream request (OutputHandleIndex, PropertyTag,
// OpenModeFlags).
func buildOpenStream(inIdx, outIdx uint8, proptag uint32, flags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropOpenStream)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint32(proptag)
	b.Uint8(flags)
	return b.Bytes()
}

// buildReadStream builds a RopReadStream request. byteCount == 0xBABE selects
// the 6-byte form, where maxByteCount follows.
func buildReadStream(inIdx uint8, byteCount uint16, maxByteCount uint32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropReadStream)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint16(byteCount)
	if byteCount == 0xBABE {
		b.Uint32(maxByteCount)
	}
	return b.Bytes()
}

// readStreamChunk dispatches one ReadStream and returns the chunk bytes.
func readStreamChunk(t *testing.T, sess *Session, streamH uint32, byteCount uint16, maxByteCount uint32) []byte {
	t.Helper()
	rs := dispatchOn(sess, buildReadStream(0, byteCount, maxByteCount), streamH)
	p := ropOK(t, rs, ropReadStream, "ReadStream")
	data, err := p.BinShort()
	if err != nil {
		t.Fatalf("ReadStream binary: %v", err)
	}
	return data
}

func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// TestOpenReadStream opens a stream over the message body and reads it back in
// two chunks, the 2-byte ByteCount form then the 0xBABE 6-byte form, checking
// the size, that the chunks reassemble to the UTF-16LE body, that a read at
// end-of-stream returns zero bytes, and that missing properties and bad handles
// are refused without allocating handles or moving the stream cursor.
func TestOpenReadStream(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedInboxMessage(t, dir, "STREAMMSG") // body = "hello body"
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]

	assertOpenStreamRefusals(t, sess, msgH)

	// OpenStream over PrBody: message input slot 0, stream output slot 1.
	os, h := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrBody), 0), []uint32{msgH, 0xFFFFFFFF})
	p := ropOK(t, os, ropOpenStream, "OpenStream")
	size := int(mustU32(t, p, "StreamSize"))
	if size == 0 || size%2 != 0 {
		t.Fatalf("StreamSize = %d, want a non-empty even UTF-16LE length", size)
	}
	streamH := h[1]

	// Read the first 4 bytes (2-byte ByteCount form), verify a non-stream handle read
	// leaves the cursor intact, then read the rest via the 0xBABE 6-byte form.
	first := readStreamChunk(t, sess, streamH, 4, 0)
	if len(first) != 4 {
		t.Fatalf("first chunk = %d bytes, want 4", len(first))
	}
	rest := readStreamChunk(t, sess, streamH, 0xBABE, 0xFFFF)
	full := append(append([]byte{}, first...), rest...)
	if len(full) != size {
		t.Errorf("reassembled %d bytes, want StreamSize %d", len(full), size)
	}
	// The seeded body is "hello body" and its line ending; nothing else may surround it.
	if got := strings.TrimRight(decodeUTF16LE(full), "\r\n"); got != "hello body" {
		t.Errorf("stream body = %q, want \"hello body\"", decodeUTF16LE(full))
	}

	// A read past the end returns zero bytes.
	if eof := readStreamChunk(t, sess, streamH, 0xFFFF, 0); len(eof) != 0 {
		t.Errorf("end-of-stream read = %d bytes, want 0", len(eof))
	}
}

func assertOpenStreamRefusals(t *testing.T, sess *Session, msgH uint32) {
	t.Helper()
	missingResp, missingHandles := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrComment), 0), []uint32{msgH, 0xFFFFFFFF})
	wantEC(t, missingResp, ropOpenStream, ecNotFound, "OpenStream missing prop")
	if len(missingHandles) != 2 || missingHandles[1] != 0xFFFFFFFF {
		t.Errorf("missing prop handles = %#x, want slot 1 unset", missingHandles)
	}
	badParentResp, badParentHandles := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrBody), 0), []uint32{0xFFFFFFFF, 0xFFFFFFFF})
	wantEC(t, badParentResp, ropOpenStream, ecError, "OpenStream null parent")
	if len(badParentHandles) != 2 || badParentHandles[1] != 0xFFFFFFFF {
		t.Errorf("null parent handles = %#x, want slot 1 unset", badParentHandles)
	}
	wantEC(t, dispatchOn(sess, buildReadStream(0, 4, 0), msgH), ropReadStream, ecError, "ReadStream on non-stream handle")
}
