package rop

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropEC reads the ReturnValue of a single-ROP response.
func ropEC(t *testing.T, resp []byte) uint32 {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	return mustU32(t, p, "ec")
}

// TestAWriteIntoAFullMailboxIsQuotaExceeded fills the mailbox past its receive
// quota, past which nothing new may enter it. Every ROP that adds an item must
// answer ecQuotaExceeded (0x4D9, [MS-OXCDATA] 2.4.1 "would have exceeded a
// resource quota") and store nothing: saving a new message, copying messages or
// a folder, and attaching to a stored message. A move inside the mailbox adds
// nothing and stays open, so a full mailbox can still file what it holds.
func TestAWriteIntoAFullMailboxIsQuotaExceeded(t *testing.T) {
	dir := provisionedDir(t)
	st, err := objectstore.Open(dir)
	mustNil(t, err)
	inbox := int64(mapi.PrivateFIDInbox)
	big := "From: s@hermex.test\r\nSubject: big\r\n\r\n" + strings.Repeat("x", 4096)
	info, err := st.AppendMessage(inbox, []byte(big), time.Unix(3, 0), 0)
	mustNil(t, err)
	src, err := st.CreateFolder(&inbox, "Src")
	mustNil(t, err)
	mustNil(t, st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}))
	st.Close()

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	junkEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDJunk))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(info.ID)))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	inboxH := h[1]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, junkEID), []uint32{logonH, 0xFFFFFFFF})
	junkH := h[1]

	_, h = sess.Dispatch(buildCreateMessage(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	save, _ := sess.Dispatch(buildSaveChangesMessage(0, 0), []uint32{h[1]})
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, msgEID), []uint32{logonH, 0xFFFFFFFF})
	attach, _ := sess.Dispatch(buildCreateAttachment(0, 1), []uint32{h[1], 0xFFFFFFFF})
	cp, _ := sess.Dispatch(buildMoveCopyMessages(1, 2, 1, msgEID), []uint32{logonH, inboxH, junkH})
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits
	cf, _ := sess.Dispatch(buildCopyFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(src))), "Copy"), []uint32{inboxH, junkH})
	for name, resp := range map[string][]byte{"SaveChangesMessage": save, "CreateAttachment": attach, "CopyMessages": cp, "CopyFolder": cf} {
		if ec := ropEC(t, resp); ec != ecQuotaExceeded {
			t.Errorf("%s into a full mailbox: ec %#x, want ecQuotaExceeded", name, ec)
		}
	}
	assertNothingAdded(t, dir)

	mv, _ := sess.Dispatch(buildMoveCopyMessages(1, 2, 0, msgEID), []uint32{logonH, inboxH, junkH})
	ropOK(t, mv, ropMoveCopyMessages, "MoveMessages in a full mailbox")
}

// TestAnICSUploadIntoAFullMailboxIsQuotaExceeded imports a new message through
// the ICS upload path (Outlook's cached-mode sync) into a mailbox past its receive
// quota: the save must answer ecQuotaExceeded and the message must not exist.
func TestAnICSUploadIntoAFullMailboxIsQuotaExceeded(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "anchor")
	st, err := objectstore.Open(dir)
	mustNil(t, err)
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: big\r\n\r\n"+strings.Repeat("x", 4096)), time.Unix(3, 0), 0)
	mustNil(t, err)
	mustNil(t, st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}))
	st.Close()

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	h, _ = mustDispatchOK(t, sess, buildOpenCollector(1, 2, 1), []uint32{logonH, folderH, 0xFFFFFFFF}, ropSyncOpenCollector)
	collectorH := h[2]

	const newMid = 0x9000
	key := homeSourceKey(t, dir, newMid)
	header := mapi.PropertyValues{
		{Tag: mapi.PrSourceKey, Value: key},
		{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Now())},
		{Tag: mapi.PrChangeKey, Value: key},
		{Tag: mapi.PrPredecessorChangeList, Value: []byte{}},
	}
	h, _ = mustDispatchOK(t, sess, buildImportMessageChange(2, 3, 0, header),
		[]uint32{logonH, folderH, collectorH, 0xFFFFFFFF}, ropSyncImportMessageChange)
	handles := []uint32{logonH, folderH, collectorH, h[3], 0xFFFFFFFF}
	h, _ = mustDispatchOK(t, sess, buildDestConfigure(3, 4, fastCopyTo), handles, ropFastTransferDestConfigure)
	handles[4] = h[4]
	mustDispatchOK(t, sess, buildDestPutBuffer(4, messageContentBody("IPM.Note", "Imported")), handles, ropFastTransferDestPutBuffer)

	save, _ := sess.Dispatch(buildSaveChangesMessage(0, 3), handles)
	if ec := ropEC(t, save); ec != ecQuotaExceeded {
		t.Errorf("ICS upload into a full mailbox: ec %#x, want ecQuotaExceeded", ec)
	}
	st, err = objectstore.Open(dir)
	mustNil(t, err)
	defer st.Close()
	if _, err := st.GetMessageProperties(newMid, mapi.PrSubject); err == nil {
		t.Error("the refused upload was stored")
	}
}

// assertNothingAdded checks the refused writes left Inbox with its one message
// and Junk with no message and no subfolder.
func assertNothingAdded(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNil(t, err)
	defer st.Close()
	inbox, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNil(t, err)
	junk, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	mustNil(t, err)
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	mustNil(t, err)
	msg, err := st.OpenMessage(inbox[0].ID)
	mustNil(t, err)
	// The folder object listing includes the one stored mail.
	if len(inbox) != 1 || len(junk) != 0 || len(objs) != 1 || len(msg.Attachments) != 0 {
		t.Errorf("refused writes stored items: inbox %d, junk %d, inbox objects %d, attachments %d",
			len(inbox), len(junk), len(objs), len(msg.Attachments))
	}
	junkFID := int64(mapi.PrivateFIDJunk)
	if _, found, err := st.FolderByName(&junkFID, "Copy"); err != nil || found {
		t.Errorf("the refused folder copy was created (err %v)", err)
	}
}

// mustNil stops the test on a setup error.
func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
