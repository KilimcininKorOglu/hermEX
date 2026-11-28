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
	mustBuild(b.PropTags(excluded))
	return b.Bytes()
}

// copyToSession seeds one rich message (a recipient and an attachment) in the inbox
// and returns a session with that message opened at handle slot 1.
func copyToSession(t *testing.T) (sess *Session, logonH, msgH uint32) {
	t.Helper()
	dir := provisionedDir(t)
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
	mustClose(t, st)

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(mid)))
	sess = NewSession(dir, accounts, "user@hermex.test")
	logonH = dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	msgH = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
	return
}

// wantLiveHandle asserts h still resolves to an object, so a refused ROP on
// another slot did not release the caller's open object.
func wantLiveHandle(t *testing.T, sess *Session, h uint32, what string) {
	t.Helper()
	if sess.get(h) == nil {
		t.Errorf("%s: handle %#x no longer resolves to an object", what, h)
	}
}

// requireHandles fails the test when a setup ROP left a handle unset, so a
// refusal asserted later is the handler's own and not one for a null handle.
func requireHandles(t *testing.T, what string, handles ...uint32) {
	t.Helper()
	for i, h := range handles {
		if h == 0xFFFFFFFF {
			t.Fatalf("%s: handle %d was not set", what, i)
		}
	}
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
	sess, logonH, msgH := copyToSession(t)
	defer sess.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	folderH := dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)[1]
	defer wantLiveHandle(t, sess, msgH, "CopyTo(folder) message handle")
	defer wantLiveHandle(t, sess, folderH, "CopyTo(folder) source handle")
	handles := []uint32{logonH, folderH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildFastCopyTo(1, 2, 0, 0, nil), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecError {
		t.Fatalf("folder source ReturnValue = %#x, want ecError", ec)
	}
	wantNoDownload(t, sess, h[2], "CopyTo(folder)")
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
	// MessageIds is a 16-bit count of 8-byte EIDs.
	eids := make([]uint64, len(mids))
	for i, mid := range mids {
		eids[i] = uint64(mapi.MakeEIDEx(1, uint64(mid)))
	}
	mustBuild(b.Uint64ArrayShort(eids))
	b.Uint8(0) // CopyFlags
	b.Uint8(0) // SendOptions
	return b.Bytes()
}

// copyMessagesSession seeds two messages (one normal, one associated) in the inbox,
// returns their ids and a session with the inbox folder opened at handle slot 1.
func copyMessagesSession(t *testing.T) (sess *Session, logonH, folderH uint32, mids []int64) {
	t.Helper()
	dir := provisionedDir(t)
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
	mustClose(t, st)

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	sess = NewSession(dir, accounts, "user@hermex.test")
	logonH = dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	folderH = dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)[1]
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
	sr, h := sess.Dispatch(buildFastCopyMessages(1, 2, []int64{1}), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecError {
		t.Fatalf("message source ReturnValue = %#x, want ecError", ec)
	}
	wantNoDownload(t, sess, h[2], "CopyMessages(message)")
}

// buildFastCopyFolder frames a RopFastTransferSourceCopyFolder request: the source
// folder at inIdx, the download handle at outIdx, then CopyFlags / SendOptions.
func buildFastCopyFolder(inIdx, outIdx, copyFlags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropFastTransferSourceCopyFolder)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(copyFlags)
	b.Uint8(0) // SendOptions
	return b.Bytes()
}

// TestFastTransferSourceCopyFolder opens a topFolder download over a folder holding
// a normal and an associated message and drains it, asserting the topFolder framing:
// one STARTTOPFLD, a matching ENDFOLDER, a StartMessage for the normal message, a
// StartFAIMsg for the associated one, and no ICS synchronization framing.
func TestFastTransferSourceCopyFolder(t *testing.T) {
	sess, logonH, folderH, mids := copyMessagesSession(t)
	defer sess.Close()
	if len(mids) != 2 {
		t.Fatalf("fixture seeded %d messages, want 2", len(mids))
	}

	handles := []uint32{logonH, folderH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildFastCopyFolder(1, 2, fastCopyFolderFlagCopySubfolder), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropFastTransferSourceCopyFolder {
		t.Fatalf("CopyFolder RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("CopyFolder ReturnValue = %#x", ec)
	}

	items := drainSyncDownload(t, sess, h, 2)
	if n := ropMarkerCount(items, ics.MarkerStartTopFld); n != 1 {
		t.Errorf("StartTopFld count = %d, want 1", n)
	}
	if n := ropMarkerCount(items, ics.MarkerEndFolder); n != 1 {
		t.Errorf("EndFolder count = %d, want 1", n)
	}
	if n := ropMarkerCount(items, ics.MarkerStartMessage); n != 1 {
		t.Errorf("StartMessage count = %d, want 1 (the normal message)", n)
	}
	if n := ropMarkerCount(items, ics.MarkerStartFAIMsg); n != 1 {
		t.Errorf("StartFAIMsg count = %d, want 1 (the associated message)", n)
	}
	if n := ropMarkerCount(items, ics.MarkerIncrSyncChg); n != 0 {
		t.Errorf("topFolder carried %d INCRSYNCCHG markers, want 0 (no sync framing)", n)
	}
}

// TestFastTransferSourceCopyFolderRejectsMessage asserts a message handle as the
// source is refused: CopyFolder streams a folder subtree.
func TestFastTransferSourceCopyFolderRejectsMessage(t *testing.T) {
	sess, logonH, msgH := copyToSession(t)
	defer sess.Close()

	handles := []uint32{logonH, msgH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildFastCopyFolder(1, 2, 0), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecError {
		t.Fatalf("message source ReturnValue = %#x, want ecError", ec)
	}
	wantNoDownload(t, sess, h[2], "CopyFolder(message)")
}

// wantNoDownload asserts a refused FastTransfer source left the output slot
// without a live download object, so the client holds nothing to drain.
func wantNoDownload(t *testing.T, sess *Session, outH uint32, what string) {
	t.Helper()
	if outH != 0xFFFFFFFF && sess.get(outH) != nil {
		t.Errorf("%s: the refused source still minted download handle %#x", what, outH)
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
	mustBuild(b.PropTags(included))
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
	sess, logonH, msgH := copyToSession(t)
	defer sess.Close()
	defer wantLiveHandle(t, sess, msgH, "Progress message handle")

	p := ext.NewPull(dispatchOn(sess, buildProgress(0, 0), logonH), ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropProgress {
		t.Fatalf("Progress RopId = %#x", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotSupported {
		t.Fatalf("Progress ReturnValue = %#x, want ecNotSupported", ec)
	}
}
