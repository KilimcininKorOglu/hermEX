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
// that addresses the message, deliberately distinct so the two-handle resolution
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
	mustBuild(b.Uint64ArrayShort(eids))
	return b.Bytes()
}

// buildSetReadFlags builds a RopSetReadFlags request over a folder handle: the
// ReadFlags byte and the message-id list (empty => every message in the folder).
func buildSetReadFlags(inIdx, readFlags uint8, eids ...uint64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSetReadFlags)
	b.Uint8(0)         // LogonId
	b.Uint8(inIdx)     // folder handle
	b.Uint8(0)         // WantAsynchronous
	b.Uint8(readFlags) // ReadFlags
	mustBuild(b.Uint64ArrayShort(eids))
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
	mustBuild(b.Uint64ArrayShort(eids))
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
	defer mustClose(t, st)
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
	dir := provisionedDir(t)
	msgID := seedInboxMessage(t, dir, "READFLAG")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(msgID)))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)
	msgH := h[1]

	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen != 0 {
		t.Fatalf("seeded message is already read (flags=%#x), want unread", f)
	}

	// rfGenerateReceiptOnly manages a read receipt only; from the unread seed the
	// message must stay unread. A per-bit test on rfClearReadFlag would wrongly
	// mark it read here, so this guards the exact-value dispatch.
	ropOK(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfGenerateReceiptOnly), logonH, msgH), ropSetMessageReadFlag, "SetMessageReadFlag(rfGenerateReceiptOnly)")
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen != 0 {
		t.Errorf("rfGenerateReceiptOnly changed read state (flags=%#x), want still unread", f)
	}

	// Mark read (rfDefault). The message is at slot 1; the header handle is slot 0.
	sr := dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfDefault), logonH, msgH)
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
	ropOK(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfClearNotifyRead), logonH, msgH), ropSetMessageReadFlag, "SetMessageReadFlag(rfClearNotifyRead)")
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen == 0 {
		t.Errorf("rfClearNotifyRead changed read state (flags=%#x), want still read", f)
	}

	// Mark unread (rfClearReadFlag).
	ropOK(t, dispatchOn(sess, buildSetMessageReadFlag(0, 1, rfClearReadFlag), logonH, msgH), ropSetMessageReadFlag, "SetMessageReadFlag(rfClearReadFlag)")
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), msgID); f&objectstore.FlagSeen != 0 {
		t.Errorf("after mark-unread, flags=%#x, want FlagSeen clear", f)
	}
}

// TestDeleteMessages deletes one of two messages from a folder through the ROP
// and confirms the targeted message is gone, the other remains, and the response
// reports no partial completion.
func TestDeleteMessages(t *testing.T) {
	dir := provisionedDir(t)
	id1 := seedInboxMessage(t, dir, "DELME")
	id2 := seedInboxMessage(t, dir, "KEEPME")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	folderH := h[1]

	del := dispatchOn(sess, buildDeleteMessages(0, uint64(mapi.MakeEIDEx(1, uint64(id1)))), folderH)
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
	if dump := mustListSoftDeleted(t, sess.get(folderH).store, int64(mapi.PrivateFIDInbox)); len(dump) != 1 {
		t.Errorf("dumpster = %d, want 1 (deleted message recoverable)", len(dump))
	}
}

// TestFolderRopsLeaveAMessageOfAnotherFolder proves DeleteMessages,
// HardDeleteMessages and SetReadFlags act only on messages of the folder the
// handle opened. The rights check runs on that folder while the message ids come
// from the client, so an Inbox message named over a Sent Items handle must stay
// as it is, and the response reports PartialCompletion as [MS-OXCFOLD] 2.2.1.11,
// 2.2.1.12 and 2.2.1.2 require for a message the server did not process.
func TestFolderRopsLeaveAMessageOfAnotherFolder(t *testing.T) {
	dir := provisionedDir(t)
	mid := seedInboxMessage(t, dir, "Inbox mail")
	eid := uint64(mapi.MakeEIDEx(1, uint64(mid)))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDSentItems))), ropOpenFolder, "OpenFolder(SentItems)", h[0], 0xFFFFFFFF)
	sentH := h[1]

	hard := ext.NewPush(ext.FlagUTF16)
	hard.Uint8(0) // WantAsynchronous
	hard.Uint8(0) // NotifyNonRead
	mustBuild(hard.Uint64ArrayShort([]uint64{eid}))
	cases := []struct {
		name  string
		ropID uint8
		req   []byte
	}{
		{"DeleteMessages", ropDeleteMessages, buildDeleteMessages(0, eid)},
		{"HardDeleteMessages", ropHardDeleteMessages, toROPRequest(ropHardDeleteMessages, 0, hard.Bytes())},
		{"SetReadFlags", ropSetReadFlags, buildSetReadFlags(0, rfDefault, eid)},
	}
	for _, c := range cases {
		resp := dispatchOn(sess, c.req, sentH)
		p := ext.NewPull(resp, ext.FlagUTF16)
		if id := mustU8(t, p, "RopId"); id != c.ropID {
			t.Fatalf("%s RopId = %#x", c.name, id)
		}
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Errorf("%s ReturnValue = %#x, want success with partial completion", c.name, ec)
			continue
		}
		if pc := mustU8(t, p, "partialCompletion"); pc != 1 {
			t.Errorf("%s PartialCompletion = %d, want 1", c.name, pc)
		}
	}
	f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), mid)
	if f == -1 {
		t.Fatal("the Inbox message was deleted through a Sent Items handle")
	}
	if f&objectstore.FlagSeen != 0 {
		t.Errorf("the Inbox message was marked read through a Sent Items handle (flags=%#x)", f)
	}
}

// TestSetReadFlags marks messages read in bulk over a folder handle: an explicit
// two-id list flips both to read, then an empty list (meaning every message in the
// folder) with rfClearReadFlag flips them back to unread. This proves both the
// explicit-list and the empty-means-all paths and both flag directions.
func TestSetReadFlags(t *testing.T) {
	dir := provisionedDir(t)
	id1 := seedInboxMessage(t, dir, "BULK1")
	id2 := seedInboxMessage(t, dir, "BULK2")
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	folderH := h[1]

	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), id1); f&objectstore.FlagSeen != 0 {
		t.Fatalf("seeded message id1 already read (flags=%#x), want unread", f)
	}

	// Mark both read via an explicit id list (rfDefault).
	sr := dispatchOn(sess, buildSetReadFlags(0, rfDefault,
		uint64(mapi.MakeEIDEx(1, uint64(id1))), uint64(mapi.MakeEIDEx(1, uint64(id2)))), folderH)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSetReadFlags {
		t.Fatalf("SetReadFlags RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SetReadFlags(read) ReturnValue = %#x", ec)
	}
	if pc := mustU8(t, p, "partialCompletion"); pc != 0 {
		t.Errorf("PartialCompletion = %d, want 0", pc)
	}
	for _, id := range []int64{id1, id2} {
		if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), id); f&objectstore.FlagSeen == 0 {
			t.Errorf("message %d not marked read (flags=%#x)", id, f)
		}
	}

	// Mark all unread via an EMPTY id list (every message in the folder), rfClearReadFlag.
	clr := ropOK(t, dispatchOn(sess, buildSetReadFlags(0, rfClearReadFlag), folderH), ropSetReadFlags, "SetReadFlags(empty list, clear)")
	wantU8(t, clr, "empty-list PartialCompletion", 0)
	for _, id := range []int64{id1, id2} {
		if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), id); f&objectstore.FlagSeen != 0 {
			t.Errorf("message %d still read after empty-list clear (flags=%#x)", id, f)
		}
	}
}

// TestMoveCopyMessages moves a message Inbox->Junk (preserving its read flag and
// received date), then copies one Inbox->Junk (leaving the source), and confirms
// a message id absent from the source folder reports partial completion.
func TestMoveCopyMessages(t *testing.T) {
	dir := provisionedDir(t)
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
	mustClose(t, st)

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	junkEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDJunk))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	inboxH := h[1]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, junkEID), ropOpenFolder, "OpenFolder(Junk)", logonH, 0xFFFFFFFF)
	junkH := h[1]

	// Move MOVEME (Inbox slot 1 -> Junk slot 2), plus a bogus id to force partial.
	mv := dispatchOn(sess,
		buildMoveCopyMessages(1, 2, 0, uint64(mapi.MakeEIDEx(1, uint64(moveInfo.ID))), uint64(mapi.MakeEIDEx(1, 999999))),
		logonH, inboxH, junkH)
	p := ropOK(t, mv, ropMoveCopyMessages, "MoveCopyMessages")
	wantU8(t, p, "PartialCompletion (the bogus id could not be moved)", 1)
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), moveInfo.ID); f != -1 {
		t.Errorf("moved message still in source folder")
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st2)
	assertMovedCopyKeptState(t, st2, date)

	// Copy COPYME (Inbox -> Junk): the source copy must remain.
	cp := dispatchOn(sess,
		buildMoveCopyMessages(1, 2, 1, uint64(mapi.MakeEIDEx(1, uint64(copyInfo.ID)))),
		logonH, inboxH, junkH)
	p = ropOK(t, cp, ropMoveCopyMessages, "MoveCopyMessages(copy)")
	wantU8(t, p, "copy PartialCompletion", 0)
	if f := messageFlags(t, dir, int64(mapi.PrivateFIDInbox), copyInfo.ID); f == -1 {
		t.Errorf("copied message was removed from the source folder")
	}
	if junk := mustListMessages(t, st2, int64(mapi.PrivateFIDJunk)); len(junk) != 2 {
		t.Errorf("Junk has %d messages after copy, want 2", len(junk))
	}
}

// assertMovedCopyKeptState checks the message a move landed in Junk. It arrives
// under a fresh id, so it is found by being the only one there, and its read
// flag and received date must have survived the re-file.
func assertMovedCopyKeptState(t *testing.T, st *objectstore.Store, date time.Time) {
	t.Helper()
	junk, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
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
}
