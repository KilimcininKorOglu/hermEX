package rop

import (
	"errors"
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
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	destEID := uint64(mapi.MakeEIDEx(1, destFID))
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, destEID), ropOpenFolder, "OpenFolder", logonH, 0xFFFFFFFF)
	folderH := h[1]
	h = dispatchOK(t, sess, buildOpenCollector(1, 2, 1), ropSyncOpenCollector, "OpenCollector", logonH, folderH, 0xFFFFFFFF)
	return sess, []uint32{logonH, folderH, h[2]}
}

// TestSyncImportMessageMove drives the full move ROP over an upload collector opened
// on the destination folder. The request's id XIDs name the source folder, the source
// message, and a client-chosen destination id; the seeded inbox message must relocate
// into the destination folder under that id, the response carries a zero message id
// (the client's id is authoritative), and a no-op ROP chained behind the move only
// frames if the five length-prefixed binaries were consumed exactly.
func TestSyncImportMessageMove(t *testing.T) {
	dir := provisionedDir(t)
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
	sr := dispatchOn(sess, batch, handles...)

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

	// The message leaves the Inbox and is filed in the destination folder under
	// the client's id.
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f != -1 {
		t.Errorf("source message %#x is still in the Inbox after the move", msgID)
	}
	if fid := storedMessageFolder(t, dir, int64(dstMID)); fid != int64(destFID) {
		t.Errorf("destination message %#x is in folder %d after the move, want Sent Items", dstMID, fid)
	}
}

// storedMessageFolder reads the folder a message row is filed in, or -1 when the
// store holds no row under that id.
func storedMessageFolder(t *testing.T, dir string, mid int64) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	fid, err := st.MessageFolder(mid)
	if errors.Is(err, objectstore.ErrNotFound) {
		return -1
	}
	if err != nil {
		t.Fatal(err)
	}
	return fid
}

// TestSyncImportMessageMoveNeedsDeleteOnTheSource proves a delegate's move is gated
// on the source folder as well as the destination. The source folder comes from
// the client's request, so a delegate with Editor rights on Sent Items only must
// not pull a message out of the boss's Inbox: the move answers ecAccessDenied
// (MAPI_E_NO_ACCESS) and the message stays where it was.
func TestSyncImportMessageMoveNeedsDeleteOnTheSource(t *testing.T) {
	bossDir := provisionedDir(t)
	msgID := seedInboxMessage(t, bossDir, "PRIVATE")
	grantFolderPermission(t, bossDir, int64(mapi.PrivateFIDSentItems), "delegate@hermex.test", mapi.RightsEditor)
	sess, logonH := delegateLogon(t, bossDir, "delegate@hermex.test")
	defer sess.Close()
	h := dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDSentItems))), ropOpenFolder, "OpenFolder(SentItems)", logonH, 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildOpenCollector(1, 2, 1), ropSyncOpenCollector, "OpenCollector", logonH, h[1], 0xFFFFFFFF)
	handles := []uint32{logonH, h[1], h[2]}

	dstMID := uint64(msgID) + 0x100000
	move := buildSyncImportMessageMove(2,
		homeSourceKey(t, bossDir, uint64(mapi.PrivateFIDInbox)), homeSourceKey(t, bossDir, uint64(msgID)),
		homeSourceKey(t, bossDir, uint64(msgID)), homeSourceKey(t, bossDir, dstMID), homeSourceKey(t, bossDir, dstMID)[16:])
	sr := dispatchOn(sess, move, handles...)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSynchronizationImportMessageMove {
		t.Fatalf("RopId = %#x, want %#x", id, ropSynchronizationImportMessageMove)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecAccessDenied {
		t.Errorf("move out of an ungranted folder ec = %#x, want ecAccessDenied", ec)
	}
	if f := messageFlags(t, bossDir, int64(mapi.PrivateFIDInbox), msgID); f == -1 {
		t.Error("the boss's Inbox message left the Inbox")
	}
	if fid := storedMessageFolder(t, bossDir, int64(dstMID)); fid != -1 {
		t.Error("the refused move still filed a message in Sent Items")
	}
}

// TestSyncImportMessageMoveObjectDeleted asserts the handler maps a source the store
// no longer holds to SYNC_E_OBJECT_DELETED rather than a generic failure.
func TestSyncImportMessageMoveObjectDeleted(t *testing.T) {
	dir := provisionedDir(t)
	anchor := seedInboxMessage(t, dir, "anchor") // materializes the store; not the move target
	destFID := uint64(mapi.PrivateFIDSentItems)

	sess, handles := openMoveCollector(t, dir, destFID)
	defer sess.Close()

	srcFolder := homeSourceKey(t, dir, uint64(mapi.PrivateFIDInbox))
	srcMsg := homeSourceKey(t, dir, 0x7654321) // no such message
	dstMsg := homeSourceKey(t, dir, 0x100000)
	cl := homeSourceKey(t, dir, 1)
	cn := homeSourceKey(t, dir, 1)[16:]

	move := buildSyncImportMessageMove(2, srcFolder, srcMsg, cl, dstMsg, cn)
	sr := dispatchOn(sess, move, handles...)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSynchronizationImportMessageMove {
		t.Fatalf("RopId = %#x, want %#x", id, ropSynchronizationImportMessageMove)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSyncObjectDel {
		t.Fatalf("absent-source move ReturnValue = %#x, want SYNC_E_OBJECT_DELETED %#x", ec, ecSyncObjectDel)
	}
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), anchor); f == -1 {
		t.Error("absent-source move removed the unrelated anchor message")
	}
	if fid := storedMessageFolder(t, dir, 0x100000); fid != -1 {
		t.Errorf("absent-source move still filed destination message in folder %d", fid)
	}
}
