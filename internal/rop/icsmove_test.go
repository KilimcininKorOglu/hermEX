package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildSyncImportMessageMove frames a RopSynchronizationImportMessageMove request:
// the RopId/LogonId/InputHandleIndex head followed by five 32-bit-length-prefixed
// binaries (source folder, source message, predecessor change list, destination
// message, change number).
func buildSyncImportMessageMove(inIdx uint8, srcFolder, srcMsg, changeList, dstMsg, changeNum []byte) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSynchronizationImportMessageMove)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.BinEx(srcFolder)
	b.BinEx(srcMsg)
	b.BinEx(changeList)
	b.BinEx(dstMsg)
	b.BinEx(changeNum)
	return b.Bytes()
}

// openMoveCollector opens a logon plus a contents-upload collector on the
// destination folder, returning the session and the [logon, folder, collector]
// handle slots the move ROP addresses.
func openMoveCollector(t *testing.T, dir string, destFID uint64) (*Session, []uint32) {
	t.Helper()
	sess := NewSession(dir, nil, "")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	destEID := uint64(mapi.MakeEIDEx(1, destFID))
	_, h = sess.Dispatch(buildOpenFolder(0, 1, destEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	handles := []uint32{logonH, folderH, 0xFFFFFFFF}
	h, _ = mustDispatchOK(t, sess, buildOpenCollector(1, 2, 1), handles, ropSyncOpenCollector)
	return sess, []uint32{logonH, folderH, h[2]}
}

// TestSyncImportMessageMove drives the full move ROP over an upload collector opened
// on the destination folder. The request's id XIDs name the source folder, the source
// message, and a client-chosen destination id; the seeded inbox message must relocate
// into the destination folder under that id, the response carries a zero message id
// (the client's id is authoritative), and a no-op ROP chained behind the move only
// frames if the five length-prefixed binaries were consumed exactly.
func TestSyncImportMessageMove(t *testing.T) {
	dir := t.TempDir()
	msgID := seedInboxMessage(t, dir, "MOVEME")
	destFID := uint64(mapi.PrivateFIDSentItems)
	dstMID := uint64(msgID) + 0x100000

	sess, handles := openMoveCollector(t, dir, destFID)
	defer sess.Close()

	srcFolder := homeSourceKey(t, dir, uint64(mapi.PrivateFIDInbox))
	srcMsg := homeSourceKey(t, dir, uint64(msgID))
	changeList := homeSourceKey(t, dir, uint64(msgID)) // predecessor change list, accepted but not compared
	dstMsg := homeSourceKey(t, dir, dstMID)
	changeNum := homeSourceKey(t, dir, dstMID)[16:] // a 6-byte change number whose content is irrelevant

	move := buildSyncImportMessageMove(2, srcFolder, srcMsg, changeList, dstMsg, changeNum)
	batch := append(move, buildSetLocalReplicaMidsetDeleted(1)...)
	sr, _ := sess.Dispatch(batch, handles)

	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSynchronizationImportMessageMove {
		t.Fatalf("RopId = %#x, want %#x", id, ropSynchronizationImportMessageMove)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("move ReturnValue = %#x, want ecSuccess", ec)
	}
	if mid := mustU64(t, p, "MessageId"); mid != 0 {
		t.Errorf("response MessageId = %#x, want 0 (client id authoritative)", mid)
	}
	if id := mustU8(t, p, "second RopId"); id != ropSetLocalReplicaMidsetDeleted {
		t.Fatalf("second response RopId = %#x; move over/under-consumed its request", id)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.OpenMessage(int64(msgID)); err == nil {
		t.Errorf("source message %#x still present in the store after move", msgID)
	}
	if _, err := st.OpenMessage(int64(dstMID)); err != nil {
		t.Fatalf("destination message %#x not found after move: %v", dstMID, err)
	}
}

// TestSyncImportMessageMoveObjectDeleted asserts the handler maps a source the store
// no longer holds to SYNC_E_OBJECT_DELETED rather than a generic failure.
func TestSyncImportMessageMoveObjectDeleted(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "anchor") // materializes the store; not the move target
	destFID := uint64(mapi.PrivateFIDSentItems)

	sess, handles := openMoveCollector(t, dir, destFID)
	defer sess.Close()

	srcFolder := homeSourceKey(t, dir, uint64(mapi.PrivateFIDInbox))
	srcMsg := homeSourceKey(t, dir, 0x7654321) // no such message
	dstMsg := homeSourceKey(t, dir, 0x100000)
	cl := homeSourceKey(t, dir, 1)
	cn := homeSourceKey(t, dir, 1)[16:]

	move := buildSyncImportMessageMove(2, srcFolder, srcMsg, cl, dstMsg, cn)
	sr, _ := sess.Dispatch(move, handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSyncObjectDel {
		t.Fatalf("absent-source move ReturnValue = %#x, want SYNC_E_OBJECT_DELETED %#x", ec, ecSyncObjectDel)
	}
}
