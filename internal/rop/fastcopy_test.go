package rop

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// buildFastCopyTo frames a RopFastTransferSourceCopyTo request: the source object at
// inIdx, the new download handle at outIdx, then Level / CopyFlags / SendOptions and
// the excluded property tags.
func buildFastCopyTo(inIdx, outIdx uint8, level uint8, copyFlags uint32, excluded []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropFastTransferSourceCopyTo)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(level)
	b.Uint32(copyFlags)
	b.Uint8(0) // SendOptions
	_ = b.PropTags(excluded)
	return b.Bytes()
}

// copyToSession seeds one rich message (a recipient and an attachment) in the inbox
// and returns a session with that message opened at handle slot 1.
func copyToSession(t *testing.T) (sess *Session, logonH, msgH uint32) {
	t.Helper()
	dir := t.TempDir()
	accounts := directory.StaticAccounts{"user@hermex.test": {MailboxPath: dir}}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	msg := &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: "copyto"},
			{Tag: mapi.PrBody, Value: "body of copyto"},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrDisplayName, Value: "Alice"},
			{Tag: mapi.PrEmailAddress, Value: "alice@example.com"},
		}},
		Attachments: []oxcmail.Attachment{{Props: mapi.PropertyValues{
			{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
			{Tag: mapi.PrAttachLongFilename, Value: "a.txt"},
			{Tag: mapi.PrAttachDataBin, Value: []byte("payload bytes")},
		}}},
	}
	mid, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), msg)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(mid)))
	sess = NewSession(dir, accounts, "user@hermex.test")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH = h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	msgH = h[1]
	return
}

// TestFastTransferSourceCopyTo opens a generic-copy download on a stored message and
// drains it, asserting the messageContent carries the recipient and attachment
// sub-object markers and none of the ICS synchronization framing (no INCRSYNCCHG).
func TestFastTransferSourceCopyTo(t *testing.T) {
	sess, logonH, msgH := copyToSession(t)
	defer sess.Close()

	handles := []uint32{logonH, msgH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildFastCopyTo(1, 2, 0, 0, nil), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropFastTransferSourceCopyTo {
		t.Fatalf("CopyTo RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CopyTo ReturnValue = %#x", ec)
	}

	items := drainSyncDownload(t, sess, h, 2)
	if n := ropMarkerCount(items, ics.MarkerStartRecip); n != 1 {
		t.Errorf("StartRecip count = %d, want 1", n)
	}
	if n := ropMarkerCount(items, ics.MarkerNewAttach); n != 1 {
		t.Errorf("NewAttach count = %d, want 1", n)
	}
	if n := ropMarkerCount(items, ics.MarkerIncrSyncChg); n != 0 {
		t.Errorf("generic copy carried %d INCRSYNCCHG markers, want 0 (no sync framing)", n)
	}
}

// TestFastTransferSourceCopyToRejectsFolder asserts a non-message source (a folder
// handle) is refused in v1 rather than silently mishandled.
func TestFastTransferSourceCopyToRejectsFolder(t *testing.T) {
	sess, logonH, _ := copyToSession(t)
	defer sess.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h := sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	handles := []uint32{logonH, folderH, 0xFFFFFFFF}
	sr, _ := sess.Dispatch(buildFastCopyTo(1, 2, 0, 0, nil), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecError {
		t.Fatalf("folder source ReturnValue = %#x, want ecError", ec)
	}
}

// buildFastCopyMessages frames a RopFastTransferSourceCopyMessages request: the
// source folder at inIdx, the download handle at outIdx, the message-id array, then
// CopyFlags / SendOptions.
func buildFastCopyMessages(inIdx, outIdx uint8, mids []int64) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropFastTransferSourceCopyMessages)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	eids := make([]mapi.EID, len(mids))
	for i, mid := range mids {
		eids[i] = mapi.MakeEIDEx(1, uint64(mid))
	}
	_ = b.EIDs(eids)
	b.Uint8(0) // CopyFlags
	b.Uint8(0) // SendOptions
	return b.Bytes()
}

// copyMessagesSession seeds two messages (one normal, one associated) in the inbox,
// returns their ids and a session with the inbox folder opened at handle slot 1.
func copyMessagesSession(t *testing.T) (sess *Session, logonH, folderH uint32, mids []int64) {
	t.Helper()
	dir := t.TempDir()
	accounts := directory.StaticAccounts{"user@hermex.test": {MailboxPath: dir}}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	normal := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
		{Tag: mapi.PrSubject, Value: "normal"},
	}}
	mid1, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), normal)
	if err != nil {
		t.Fatal(err)
	}
	fai := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
		{Tag: mapi.PrSubject, Value: "fai"},
		{Tag: mapi.PrAssociated, Value: true},
	}}
	mid2, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), fai)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sess = NewSession(dir, accounts, "user@hermex.test")
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH = h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH = h[1]
	return sess, logonH, folderH, []int64{mid1, mid2}
}

// TestFastTransferSourceCopyMessages opens a messageList download over a folder's
// messages and drains it, asserting the per-message framing: a StartMessage for the
// normal message, a StartFAIMsg for the associated one, two EndMessage markers, and
// no ICS synchronization framing.
func TestFastTransferSourceCopyMessages(t *testing.T) {
	sess, logonH, folderH, mids := copyMessagesSession(t)
	defer sess.Close()

	handles := []uint32{logonH, folderH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildFastCopyMessages(1, 2, mids), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropFastTransferSourceCopyMessages {
		t.Fatalf("CopyMessages RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CopyMessages ReturnValue = %#x", ec)
	}

	items := drainSyncDownload(t, sess, h, 2)
	if n := ropMarkerCount(items, ics.MarkerStartMessage); n != 1 {
		t.Errorf("StartMessage count = %d, want 1", n)
	}
	if n := ropMarkerCount(items, ics.MarkerStartFAIMsg); n != 1 {
		t.Errorf("StartFAIMsg count = %d, want 1", n)
	}
	if n := ropMarkerCount(items, ics.MarkerEndMessage); n != 2 {
		t.Errorf("EndMessage count = %d, want 2", n)
	}
	if n := ropMarkerCount(items, ics.MarkerIncrSyncChg); n != 0 {
		t.Errorf("messageList carried %d INCRSYNCCHG markers, want 0 (no sync framing)", n)
	}
}

// TestFastTransferSourceCopyMessagesRejectsMessage asserts a message handle (rather
// than a folder) as the source is refused: CopyMessages streams a folder's contents.
func TestFastTransferSourceCopyMessagesRejectsMessage(t *testing.T) {
	sess, logonH, msgH := copyToSession(t)
	defer sess.Close()

	handles := []uint32{logonH, msgH, 0xFFFFFFFF}
	sr, _ := sess.Dispatch(buildFastCopyMessages(1, 2, []int64{1}), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecError {
		t.Fatalf("message source ReturnValue = %#x, want ecError", ec)
	}
}

// buildFastCopyProps frames a RopFastTransferSourceCopyProperties request: the source
// at inIdx, the download handle at outIdx, then Level / CopyFlags / SendOptions and
// the included property tags.
func buildFastCopyProps(inIdx, outIdx uint8, included []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropFastTransferSourceCopyProperties)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(0) // Level
	b.Uint8(0) // CopyFlags
	b.Uint8(0) // SendOptions
	_ = b.PropTags(included)
	return b.Bytes()
}

// TestFastTransferSourceCopyProperties opens a property-only download on a stored
// message and drains it, asserting the propList carries no sub-object markers.
func TestFastTransferSourceCopyProperties(t *testing.T) {
	sess, logonH, msgH := copyToSession(t)
	defer sess.Close()

	handles := []uint32{logonH, msgH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildFastCopyProps(1, 2, []mapi.PropTag{mapi.PrSubject}), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropFastTransferSourceCopyProperties {
		t.Fatalf("CopyProperties RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CopyProperties ReturnValue = %#x", ec)
	}

	items := drainSyncDownload(t, sess, h, 2)
	if n := ropMarkerCount(items, ics.MarkerStartRecip); n != 0 {
		t.Errorf("CopyProperties carried %d StartRecip markers, want 0 (propList has no sub-objects)", n)
	}
	if n := ropMarkerCount(items, ics.MarkerNewAttach); n != 0 {
		t.Errorf("CopyProperties carried %d NewAttach markers, want 0", n)
	}
}

// buildProgress frames a RopProgress request: the polled object at inIdx and the
// WantCancel flag.
func buildProgress(inIdx, wantCancel uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropProgress)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(wantCancel)
	return b.Bytes()
}

// TestProgressNotSupported asserts RopProgress is answered ecNotSupported: hermEX
// runs every ROP synchronously, so there is never an asynchronous operation to poll.
func TestProgressNotSupported(t *testing.T) {
	sess, logonH, _ := copyToSession(t)
	defer sess.Close()

	sr, _ := sess.Dispatch(buildProgress(0, 0), []uint32{logonH})
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropProgress {
		t.Fatalf("Progress RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Fatalf("Progress ReturnValue = %#x, want ecNotSupported", ec)
	}
}
