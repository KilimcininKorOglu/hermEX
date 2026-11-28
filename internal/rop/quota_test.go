package rop

import (
	"errors"
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
	mustClose(t, st)

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	junkEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDJunk))
	msgEID := uint64(mapi.MakeEIDEx(1, uint64(info.ID)))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, inboxEID), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	inboxH := h[1]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, junkEID), ropOpenFolder, "OpenFolder(Junk)", logonH, 0xFFFFFFFF)
	junkH := h[1]

	h = dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)
	save := dispatchOn(sess, buildSaveChangesMessage(0, 0), h[1])
	h = dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, msgEID), ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)
	attach := dispatchOn(sess, buildCreateAttachment(0, 1), h[1], 0xFFFFFFFF)
	cp := dispatchOn(sess, buildMoveCopyMessages(1, 2, 1, msgEID), logonH, inboxH, junkH)
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits
	cf := dispatchOn(sess, buildCopyFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(src))), "Copy"), inboxH, junkH)
	for name, resp := range map[string][]byte{"SaveChangesMessage": save, "CreateAttachment": attach, "CopyMessages": cp, "CopyFolder": cf} {
		if ec := ropEC(t, resp); ec != ecQuotaExceeded {
			t.Errorf("%s into a full mailbox: ec %#x, want ecQuotaExceeded", name, ec)
		}
	}
	assertNothingAdded(t, dir)

	mv := dispatchOn(sess, buildMoveCopyMessages(1, 2, 0, msgEID), logonH, inboxH, junkH)
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
	bigInfo, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: big\r\n\r\n"+strings.Repeat("x", 4096)), time.Unix(3, 0), 0)
	mustNil(t, err)
	if bigInfo.ID == 0 {
		t.Fatal("big message append returned id 0")
	}
	mustNil(t, st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}))
	mustClose(t, st)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	logonH := h[0]
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), ropOpenFolder, "OpenFolder(Inbox)", logonH, 0xFFFFFFFF)
	folderH := h[1]
	h = dispatchOK(t, sess, buildOpenCollector(1, 2, 1), ropSyncOpenCollector, "OpenCollector", logonH, folderH, 0xFFFFFFFF)
	collectorH := h[2]

	const newMid = 0x9000
	key := homeSourceKey(t, dir, newMid)
	header := mapi.PropertyValues{
		{Tag: mapi.PrSourceKey, Value: key},
		{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Now())},
		{Tag: mapi.PrChangeKey, Value: key},
		{Tag: mapi.PrPredecessorChangeList, Value: []byte{}},
	}
	h, imp := mustDispatchOK(t, sess, buildImportMessageChange(2, 3, 0, header),
		[]uint32{logonH, folderH, collectorH, 0xFFFFFFFF}, ropSyncImportMessageChange)
	if mid := mustU64(t, imp, "MessageId"); mid != 0 {
		t.Errorf("ImportMessageChange MessageId = %#x, want 0", mid)
	}
	handles := []uint32{logonH, folderH, collectorH, h[3], 0xFFFFFFFF}
	h = dispatchOK(t, sess, buildDestConfigure(3, 4, fastCopyTo), ropFastTransferDestConfigure, "DestConfigure", handles...)
	handles[4] = h[4]
	mustDispatchOK(t, sess, buildDestPutBuffer(4, messageContentBody("IPM.Note", "Imported")), handles, ropFastTransferDestPutBuffer)

	save := dispatchOn(sess, buildSaveChangesMessage(0, 3), handles...)
	if ec := ropEC(t, save); ec != ecQuotaExceeded {
		t.Errorf("ICS upload into a full mailbox: ec %#x, want ecQuotaExceeded", ec)
	}
	st, err = objectstore.Open(dir)
	mustNil(t, err)
	defer mustClose(t, st)
	// Only "not found" proves absence; any other error is a failed read, not a refusal.
	if props, err := st.GetMessageProperties(newMid, mapi.PrSubject); !errors.Is(err, objectstore.ErrNotFound) || len(props) != 0 {
		t.Errorf("reading the refused upload: props %v err %v, want empty + ErrNotFound (nothing stored)", props, err)
	}
}

// assertNothingAdded checks the refused writes left Inbox with its one message
// and Junk with no message and no subfolder.
func assertNothingAdded(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNil(t, err)
	defer mustClose(t, st)
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
	if fid, found, err := st.FolderByName(&junkFID, "Copy"); err != nil || found || fid != 0 {
		t.Errorf("the refused folder copy was created (fid %d, found %v, err %v)", fid, found, err)
	}
}

// mustNil stops the test on a setup error.
func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
