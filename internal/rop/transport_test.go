package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildTransportHeaderOnly builds a header-only transport ROP request (RopId,
// LogonId, InputHandleIndex) — the wire shape of RopSetSpooler and
// RopGetTransportFolder, neither of which carries fields beyond the head.
func buildTransportHeaderOnly(ropID, inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropID)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	return b.Bytes()
}

// transportSession opens a session and returns its logon handle, the handle the
// transport ROPs resolve.
func transportSession(t *testing.T) (*Session, uint32) {
	t.Helper()
	sess := NewSession(t.TempDir(), nil, "")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	return sess, h[0]
}

// TestSetSpooler confirms RopSetSpooler answers a private-mailbox logon with the
// bare 6-byte head and ecSuccess, consuming no trailing bytes.
func TestSetSpooler(t *testing.T) {
	sess, logonH := transportSession(t)
	defer sess.Close()

	resp, _ := sess.Dispatch(buildTransportHeaderOnly(ropSetSpooler, 0), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSetSpooler {
		t.Fatalf("RopId = %#x, want SetSpooler", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SetSpooler ReturnValue = %#x", ec)
	}
	if p.Remaining() != 0 {
		t.Fatalf("SetSpooler response has %d trailing bytes; the contract is a bare head", p.Remaining())
	}
}

// TestGetTransportFolder confirms RopGetTransportFolder returns the Outbox folder
// id as an EID after the head — the id a client deposits outgoing mail into.
func TestGetTransportFolder(t *testing.T) {
	sess, logonH := transportSession(t)
	defer sess.Close()

	resp, _ := sess.Dispatch(buildTransportHeaderOnly(ropGetTransportFolder, 0), []uint32{logonH})
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropGetTransportFolder {
		t.Fatalf("RopId = %#x, want GetTransportFolder", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetTransportFolder ReturnValue = %#x", ec)
	}
	fid, err := p.Uint64()
	if err != nil {
		t.Fatalf("FolderId: %v", err)
	}
	if gcv := mapi.EID(fid).GCValue(); gcv != mapi.PrivateFIDOutbox {
		t.Errorf("FolderId GCV = %#x, want PrivateFIDOutbox (%#x)", gcv, mapi.PrivateFIDOutbox)
	}
	if p.Remaining() != 0 {
		t.Errorf("GetTransportFolder response has %d trailing bytes after FolderId", p.Remaining())
	}
}
