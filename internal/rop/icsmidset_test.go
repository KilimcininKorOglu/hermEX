package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildSetLocalReplicaMidsetDeleted frames a RopSetLocalReplicaMidsetDeleted request
// carrying ranges of the given total body size (the uint16-prefixed count + range
// span). The range bytes are opaque to hermEX, so they are left zero.
func buildSetLocalReplicaMidsetDeleted(rangeCount int) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetLocalReplicaMidsetDeleted)
	b.Uint8(0) // LogonId
	b.Uint8(0) // InputHandleIndex
	// body = uint32 range count + rangeCount * LongTermIdRange(GUID 16 + min 6 + max 6)
	body := make([]byte, 4+rangeCount*28)
	if rangeCount > 0 {
		body[0] = byte(rangeCount) // little-endian count
	}
	b.Uint16(uint16(len(body)))
	b.Raw(body)
	return b.Bytes()
}

// TestSetLocalReplicaMidsetDeleted asserts the no-op ROP is answered ecSuccess AND
// consumes EXACTLY its uint16-prefixed body: two requests are sent back-to-back in
// one batch, so the second response only appears if the first consumed its range
// span exactly (a short or long read would misframe the second ROP), and a truncated
// DataSize span aborts parsing cleanly without perturbing the logon handle.
func TestSetLocalReplicaMidsetDeleted(t *testing.T) {
	dir := provisionedDir(t)
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	batch := append(buildSetLocalReplicaMidsetDeleted(1), buildSetLocalReplicaMidsetDeleted(2)...)
	sr, outHandles := sess.Dispatch(batch, []uint32{logonH})
	if len(outHandles) != 1 || outHandles[0] != logonH {
		t.Fatalf("handles after batch = %#x, want [%#x]", outHandles, logonH)
	}
	p := ext.NewPull(sr, ext.FlagUTF16)
	for i := range 2 {
		if id := mustU8(t, p, "RopId"); id != ropSetLocalReplicaMidsetDeleted {
			t.Fatalf("response %d RopId = %#x, want %#x (second ROP misframed => first over/under-consumed)", i, id, ropSetLocalReplicaMidsetDeleted)
		}
		if idx := mustU8(t, p, "hindex"); idx != 0 {
			t.Fatalf("response %d HandleIndex = %d, want 0", i, idx)
		}
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Fatalf("response %d ReturnValue = %#x, want ecSuccess", i, ec)
		}
	}

	// Hostile truncated request (claims 32 payload bytes, provides 2): must abort
	// cleanly with no response and leave the logon handle usable afterwards.
	trunc := ext.NewPush(ext.FlagUTF16)
	trunc.Uint8(ropSetLocalReplicaMidsetDeleted)
	trunc.Uint8(0)
	trunc.Uint8(0)
	trunc.Uint16(32)
	trunc.Uint16(0)
	truncResp, truncHandles := sess.Dispatch(trunc.Bytes(), []uint32{logonH})
	if len(truncResp) != 0 {
		t.Errorf("truncated midset response = %x, want empty", truncResp)
	}
	if len(truncHandles) != 1 || truncHandles[0] != logonH {
		t.Errorf("truncated midset handles = %#x, want [%#x]", truncHandles, logonH)
	}

	// Logon handle remains healthy after the rejected truncated request.
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder after truncation", logonH, 0xFFFFFFFF)
}
