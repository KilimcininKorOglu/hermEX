package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

func buildLongTermIdFromId(objID uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropLongTermIdFromId)
	b.Uint8(0) // logon_id
	b.Uint8(0) // hindex -> logon handle
	b.Uint64(objID)
	return b.Bytes()
}

func buildIdFromLongTermId(guid mapi.GUID, gc mapi.GlobCnt) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropIdFromLongTermId)
	b.Uint8(0)
	b.Uint8(0)
	b.GUID(guid)
	b.Raw(gc[:])
	b.Uint16(0) // padding
	return b.Bytes()
}

// TestLongTermIdRoundTrip confirms a short-term object id converts to a long-term id
// and back to the identical id, so a client can persist a stable cross-session
// reference to a folder or message.
func TestLongTermIdRoundTrip(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "LTID") // seeds the mailbox (store GUID) and a message

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]

	objEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	lt, _ := sess.Dispatch(buildLongTermIdFromId(objEID), []uint32{logonH})
	p := ext.NewPull(lt, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropLongTermIdFromId {
		t.Fatalf("LongTermIdFromId RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("LongTermIdFromId ec = %#x", ec)
	}
	guid, err := p.GUID()
	if err != nil {
		t.Fatal(err)
	}
	gcBytes, err := p.Raw(6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Uint16(); err != nil { // padding
		t.Fatal(err)
	}
	var gc mapi.GlobCnt
	copy(gc[:], gcBytes)

	idResp, _ := sess.Dispatch(buildIdFromLongTermId(guid, gc), []uint32{logonH})
	p2 := ext.NewPull(idResp, ext.FlagUTF16)
	if id := mustU8(t, p2, "RopId"); id != ropIdFromLongTermId {
		t.Fatalf("IdFromLongTermId RopId = %#x", id)
	}
	mustU8(t, p2, "hindex")
	if ec := mustU32(t, p2, "ec"); ec != ecSuccess {
		t.Fatalf("IdFromLongTermId ec = %#x", ec)
	}
	got, err := p2.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	if got != objEID {
		t.Errorf("round-trip id = %#x, want %#x", got, objEID)
	}
}
