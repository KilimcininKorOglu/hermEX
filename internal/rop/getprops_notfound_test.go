package rop

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

// TestGetPropertiesSpecificAnswersAMissingPropertyWithNotFound proves a property
// the message lacks comes back as the NotFound error in place of a value, as
// [MS-OXCPRPT] specifies for RopGetPropertiesSpecific (the example in section
// 4.3 shows flag 0x0A with 0x8004010F), not with the table row's "unavailable"
// flag, which carries no error for the client to act on.
func TestGetPropertiesSpecificAnswersAMissingPropertyWithNotFound(t *testing.T) {
	dir := t.TempDir()
	mid := uint64(seedInboxMessage(t, dir, "present"))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{h[0], 0xFFFFFFFF})

	missing := mapi.MakeTag(0x6123, mapi.PtLong)
	out, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, []mapi.PropTag{missing}), []uint32{h[1]})
	ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(missing)")
	rest := out[6:] // after RopId, InputHandleIndex and ReturnValue
	// A flagged row (0x01) whose one column is an error (0x0A) holding NotFound.
	want := []byte{0x01, 0x0A, 0x0F, 0x01, 0x04, 0x80}
	if !bytes.HasPrefix(rest, want) {
		t.Errorf("row = % x, want % x", rest, want)
	}
}
