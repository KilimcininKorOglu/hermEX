package rop

import (
	"testing"

	"hermex/internal/ext"
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
// span exactly (a short or long read would misframe the second ROP). A bare
// single-request ecSuccess test would not catch a framing error.
func TestSetLocalReplicaMidsetDeleted(t *testing.T) {
	sess, logonH, _ := copyToSession(t)
	defer sess.Close()

	batch := append(buildSetLocalReplicaMidsetDeleted(1), buildSetLocalReplicaMidsetDeleted(2)...)
	sr, _ := sess.Dispatch(batch, []uint32{logonH})
	p := ext.NewPull(sr, ext.FlagUTF16)
	for i := range 2 {
		if id := mustU8(t, p, "RopId"); id != ropSetLocalReplicaMidsetDeleted {
			t.Fatalf("response %d RopId = %#x, want %#x (second ROP misframed => first over/under-consumed)", i, id, ropSetLocalReplicaMidsetDeleted)
		}
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Fatalf("response %d ReturnValue = %#x, want ecSuccess", i, ec)
		}
	}
}
