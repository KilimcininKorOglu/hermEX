package activesync

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// attachmentMIME is a multipart/mixed message with a text body and one base64
// text/plain attachment named report.txt (decoding to "hello world").
const attachmentMIME = "From: boss@hermex.test\r\n" +
	"To: alice@hermex.test\r\n" +
	"Subject: With attachment\r\n" +
	"Date: Mon, 15 Jun 2026 09:00:00 +0000\r\n" +
	"Message-ID: <att1@hermex.test>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"See the attached report.\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; name=\"report.txt\"\r\n" +
	"Content-Disposition: attachment; filename=\"report.txt\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"aGVsbG8gd29ybGQ=\r\n" +
	"--BOUND--\r\n"

// seedInboxAttachment appends the attachment message to the Inbox.
func seedInboxAttachment(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	when := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(attachmentMIME), when, 0); err != nil {
		t.Fatal(err)
	}
}

// TestSyncExposesAttachments proves a synced message carrying an attachment
// surfaces an AirSyncBase Attachments listing with a display name, a
// FileReference, and the by-value method.
func TestSyncExposesAttachments(t *testing.T) {
	ts, dir := seededServer(t)
	seedInboxAttachment(t, dir)

	postCommand(t, ts, "Sync", syncReq("0", ""))
	_, root := postCommand(t, ts, "Sync", syncReq("1", ""))

	coll := respColl(t, root)
	cmds := coll.Child(wbxml.ASCommands)
	if cmds == nil {
		t.Fatal("Sync returned no commands")
	}
	add := cmds.Child(wbxml.ASAdd)
	if add == nil {
		t.Fatal("Sync returned no Add")
	}
	atts := add.Child(wbxml.ASData).Child(wbxml.ABAttachments)
	if atts == nil {
		t.Fatal("a message with an attachment exposed no Attachments")
	}
	att := atts.Child(wbxml.ABAttachment)
	if att == nil {
		t.Fatal("Attachments carried no Attachment")
	}
	if name := att.ChildText(wbxml.ABAttDisplayName); name != "report.txt" {
		t.Errorf("attachment DisplayName = %q, want report.txt", name)
	}
	if ref := att.ChildText(wbxml.ABFileReference); ref == "" {
		t.Error("attachment carried no FileReference")
	}
	if m := att.ChildText(wbxml.ABMethod); m != "1" {
		t.Errorf("attachment Method = %q, want 1 (by-value)", m)
	}
}

// TestSyncNoAttachmentsWhenPlain proves a plain message exposes no Attachments
// element.
func TestSyncNoAttachmentsWhenPlain(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)

	postCommand(t, ts, "Sync", syncReq("0", ""))
	_, root := postCommand(t, ts, "Sync", syncReq("1", ""))

	add := respColl(t, root).Child(wbxml.ASCommands).Child(wbxml.ASAdd)
	if add == nil {
		t.Fatal("Sync returned no Add")
	}
	if add.Child(wbxml.ASData).Child(wbxml.ABAttachments) != nil {
		t.Error("a plain message must expose no Attachments element")
	}
}
