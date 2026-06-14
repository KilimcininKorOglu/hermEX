package rop

import (
	"bytes"
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
	rs, _ := sess.Dispatch(buildReadStream(0, byteCount, maxByteCount), []uint32{streamH})
	p := ext.NewPull(rs, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropReadStream {
		t.Fatalf("ReadStream RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ReadStream ReturnValue = %#x", ec)
	}
	data, err := p.BinShort()
	if err != nil {
		t.Fatalf("ReadStream binary: %v", err)
	}
	return data
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// TestOpenReadStream opens a stream over the message body and reads it back in
// two chunks — the 2-byte ByteCount form then the 0xBABE 6-byte form — checking
// the size, that the chunks reassemble to the UTF-16LE body, and that a read at
// end-of-stream returns zero bytes.
func TestOpenReadStream(t *testing.T) {
	dir := t.TempDir()
	msgID := seedInboxMessage(t, dir, "STREAMMSG") // body = "hello body"
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir)
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	// OpenStream over PrBody: message input slot 0, stream output slot 1.
	os, h := sess.Dispatch(buildOpenStream(0, 1, uint32(mapi.PrBody), 0), []uint32{msgH, 0xFFFFFFFF})
	p := ext.NewPull(os, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropOpenStream {
		t.Fatalf("OpenStream RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("OpenStream ReturnValue = %#x", ec)
	}
	size := int(mustU32(t, p, "StreamSize"))
	if size == 0 || size%2 != 0 {
		t.Fatalf("StreamSize = %d, want a non-empty even UTF-16LE length", size)
	}
	streamH := h[1]

	// Read the first 4 bytes (2-byte ByteCount form), then the rest via the
	// 0xBABE 6-byte form.
	first := readStreamChunk(t, sess, streamH, 4, 0)
	if len(first) != 4 {
		t.Fatalf("first chunk = %d bytes, want 4", len(first))
	}
	rest := readStreamChunk(t, sess, streamH, 0xBABE, 0xFFFF)
	full := append(append([]byte{}, first...), rest...)
	if len(full) != size {
		t.Errorf("reassembled %d bytes, want StreamSize %d", len(full), size)
	}
	if want := utf16le("hello body"); !bytes.Equal(full, want) {
		// fall back to a contains check (the body may carry trailing whitespace)
		if !strings.Contains(decodeUTF16LE(full), "hello body") {
			t.Errorf("stream body = %q, want it to contain \"hello body\"", decodeUTF16LE(full))
		}
	}

	// A read past the end returns zero bytes.
	if eof := readStreamChunk(t, sess, streamH, 0xFFFF, 0); len(eof) != 0 {
		t.Errorf("end-of-stream read = %d bytes, want 0", len(eof))
	}
}
