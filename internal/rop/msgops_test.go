package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildSetMessageReadFlag builds a RopSetMessageReadFlag request. respIdx is the
// common-header ResponseHandleIndex (echoed); msgIdx is the body InputHandleIndex
// that addresses the message — deliberately distinct so the two-handle resolution
// is exercised.
func buildSetMessageReadFlag(respIdx, msgIdx, flags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetMessageReadFlag)
	b.Uint8(0)       // LogonId
	b.Uint8(respIdx) // ResponseHandleIndex
	b.Uint8(msgIdx)  // ihindex2 (the message)
	b.Uint8(flags)   // ReadFlags
	return b.Bytes()
}

// buildDeleteMessages builds a RopDeleteMessages request over a folder handle.
func buildDeleteMessages(inIdx uint8, eids ...uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropDeleteMessages)
	b.Uint8(0)     // LogonId
	b.Uint8(inIdx) // folder handle
	b.Uint8(0)     // WantAsynchronous
	b.Uint8(0)     // NotifyNonRead
	_ = b.Uint64ArrayShort(eids)
	return b.Bytes()
}

// buildMoveCopyMessages builds a RopMoveCopyMessages request. srcIdx is the
// common-header source folder handle; dstIdx is the body destination handle.
func buildMoveCopyMessages(srcIdx, dstIdx, wantCopy uint8, eids ...uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropMoveCopyMessages)
	b.Uint8(0)      // LogonId
	b.Uint8(srcIdx) // source folder handle
	b.Uint8(dstIdx) // DestHandleIndex
	_ = b.Uint64ArrayShort(eids)
	b.Uint8(0)        // WantAsynchronous
	b.Uint8(wantCopy) // WantCopy (0 = move)
	return b.Bytes()
}

// messageFlags returns the IMAP flag mask of the message with the given object
// id in a folder, or -1 when it is not present.
func messageFlags(t *testing.T, dir string, folder, id int64) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ID == id {
			return m.Flags
		}
	}
	return -1
}

// TestSetMessageReadFlag opens a seeded (unread) message and flips its read flag
// both ways through the ROP, proving the read bit is set then cleared. The
// message is seated at a handle slot distinct from the echoed ResponseHandleIndex
// so resolving it at the wrong handle would fail.
func TestSetMessageReadFlag(t *testing.T) {
	dir := t.TempDir()
	msgID := seedInboxMessage(t, dir, "READFLAG")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]

	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen != 0 {
		t.Fatalf("seeded message is already read (flags=%#x), want unread", f)
	}

	// rfGenerateReceiptOnly manages a read receipt only; from the unread seed the
	// message must stay unread. A per-bit test on rfClearReadFlag would wrongly
	// mark it read here, so this guards the exact-value dispatch.
	sess.Dispatch(buildSetMessageReadFlag(0, 1, rfGenerateReceiptOnly), []uint32{logonH, msgH})
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen != 0 {
		t.Errorf("rfGenerateReceiptOnly changed read state (flags=%#x), want still unread", f)
	}

	// Mark read (rfDefault). The message is at slot 1; the header handle is slot 0.
	sr, _ := sess.Dispatch(buildSetMessageReadFlag(0, 1, rfDefault), []uint32{logonH, msgH})
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSetMessageReadFlag {
		t.Fatalf("SetMessageReadFlag RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SetMessageReadFlag(read) ReturnValue = %#x", ec)
	}
	mustU8(t, p, "readChanged") // always 0 for a private-mailbox logon
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen == 0 {
		t.Errorf("after mark-read, flags=%#x, want FlagSeen set", f)
	}

	// rfClearNotifyRead clears a pending notification only; the message stays read.
	sess.Dispatch(buildSetMessageReadFlag(0, 1, rfClearNotifyRead), []uint32{logonH, msgH})
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen == 0 {
		t.Errorf("rfClearNotifyRead changed read state (flags=%#x), want still read", f)
	}

	// Mark unread (rfClearReadFlag).
	sess.Dispatch(buildSetMessageReadFlag(0, 1, rfClearReadFlag), []uint32{logonH, msgH})
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen != 0 {
		t.Errorf("after mark-unread, flags=%#x, want FlagSeen clear", f)
	}
}

// TestDeleteMessages deletes one of two messages from a folder through the ROP
// and confirms the targeted message is gone, the other remains, and the response
// reports no partial completion.
func TestDeleteMessages(t *testing.T) {
	dir := t.TempDir()
	id1 := seedInboxMessage(t, dir, "DELME")
	id2 := seedInboxMessage(t, dir, "KEEPME")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	del, _ := sess.Dispatch(buildDeleteMessages(0, uint64(mapi.MakeEIDEx(1, uint64(id1)))), []uint32{folderH})
	p := ext.NewPull(del, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropDeleteMessages {
		t.Fatalf("DeleteMessages RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("DeleteMessages ReturnValue = %#x", ec)
	}
	if pc := mustU8(t, p, "partialCompletion"); pc != 0 {
		t.Errorf("PartialCompletion = %d, want 0", pc)
	}
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), id1); f != -1 {
		t.Errorf("deleted message still present (flags=%#x)", f)
	}
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), id2); f == -1 {
		t.Errorf("untargeted message was also deleted")
	}
	// The deleted message went to the dumpster, recoverable, not purged.
	if dump, _ := sess.get(folderH).store.ListSoftDeleted(int64(mapi.PrivateFIDInbox)); len(dump) != 1 {
		t.Errorf("dumpster = %d, want 1 (deleted message recoverable)", len(dump))
	}
}

// TestMoveCopyMessages moves a message Inbox->Junk (preserving its read flag and
// received date), then copies one Inbox->Junk (leaving the source), and confirms
// a message id absent from the source folder reports partial completion.
func TestMoveCopyMessages(t *testing.T) {
	dir := t.TempDir()
	date := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	// Seed two messages directly so the move case carries a known read flag+date.
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := func(s string) []byte {
		return []byte("From: s@hermex.test\r\nTo: a@hermex.test\r\nSubject: " + s +
			"\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody\r\n")
	}
	moveInfo, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw("MOVEME"), date, objectstore.FlagSeen)
	if err != nil {
		t.Fatal(err)
	}
	copyInfo, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw("COPYME"), date, 0)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	junkEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDJunk))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, junkEID), []uint32{logonH, 0xFFFFFFFF})
	junkH := h[1]

	// Move MOVEME (Inbox slot 1 -> Junk slot 2), plus a bogus id to force partial.
	mv, _ := sess.Dispatch(
		buildMoveCopyMessages(1, 2, 0, uint64(mapi.MakeEIDEx(1, uint64(moveInfo.ID))), uint64(mapi.MakeEIDEx(1, 999999))),
		[]uint32{logonH, inboxH, junkH})
	p := ext.NewPull(mv, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropMoveCopyMessages {
		t.Fatalf("MoveCopyMessages RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("MoveCopyMessages ReturnValue = %#x", ec)
	}
	if pc := mustU8(t, p, "partialCompletion"); pc != 1 {
		t.Errorf("PartialCompletion = %d, want 1 (the bogus id could not be moved)", pc)
	}
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), moveInfo.ID); f != -1 {
		t.Errorf("moved message still in source folder")
	}
	// The moved copy lands in Junk under a fresh id; find it by subject + assert
	// its read flag and received date survived the move.
	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	junk, err := st2.ListMessages(int64(mapi.PrivateFIDJunk))
	if err != nil {
		t.Fatal(err)
	}
	if len(junk) != 1 {
		t.Fatalf("Junk has %d messages after move, want 1", len(junk))
	}
	if junk[0].Flags&objectstore.FlagSeen == 0 {
		t.Errorf("moved message lost its read flag (flags=%#x)", junk[0].Flags)
	}
	if junk[0].InternalDate.Unix() != date.Unix() {
		t.Errorf("moved message date = %v, want %v", junk[0].InternalDate.UTC(), date)
	}

	// Copy COPYME (Inbox -> Junk): the source copy must remain.
	cp, _ := sess.Dispatch(
		buildMoveCopyMessages(1, 2, 1, uint64(mapi.MakeEIDEx(1, uint64(copyInfo.ID)))),
		[]uint32{logonH, inboxH, junkH})
	p = ext.NewPull(cp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("MoveCopyMessages(copy) ReturnValue = %#x", ec)
	}
	if pc := mustU8(t, p, "partialCompletion"); pc != 0 {
		t.Errorf("copy PartialCompletion = %d, want 0", pc)
	}
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), copyInfo.ID); f == -1 {
		t.Errorf("copied message was removed from the source folder")
	}
	if junk, _ := st2.ListMessages(int64(mapi.PrivateFIDJunk)); len(junk) != 2 {
		t.Errorf("Junk has %d messages after copy, want 2", len(junk))
	}
}
