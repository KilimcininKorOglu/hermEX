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

// TestItemOperationsFetchAttachment proves the FileReference a Sync exposed
// fetches the attachment's decoded content and content type through
// ItemOperations.
func TestItemOperationsFetchAttachment(t *testing.T) {
	ts, dir := seededServer(t)
	seedInboxAttachment(t, dir)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	_, sync := postCommand(t, ts, "Sync", syncReq("1", ""))
	att := respColl(t, sync).Child(wbxml.ASCommands).Child(wbxml.ASAdd).
		Child(wbxml.ASData).Child(wbxml.ABAttachments).Child(wbxml.ABAttachment)
	ref := att.ChildText(wbxml.ABFileReference)
	if ref == "" {
		t.Fatal("Sync exposed no FileReference to fetch")
	}

	req := wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Elem(wbxml.IOFetch,
			wbxml.Str(wbxml.IOStore, "Mailbox"),
			wbxml.Str(wbxml.ABFileReference, ref)))
	_, root := postCommand(t, ts, "ItemOperations", req)

	fetch := root.Child(wbxml.IOResponse).Child(wbxml.IOFetch)
	if s := fetch.ChildText(wbxml.IOStatus); s != "1" {
		t.Fatalf("Fetch Status = %q, want 1", s)
	}
	if fetch.ChildText(wbxml.ABFileReference) != ref {
		t.Error("Fetch did not echo the FileReference")
	}
	props := fetch.Child(wbxml.IOProperties)
	if ct := props.ChildText(wbxml.ABContentType); ct != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", ct)
	}
	data := props.Child(wbxml.IOData)
	got := ""
	if data != nil {
		got = string(data.Opaque)
	}
	if got != "hello world" {
		t.Errorf("attachment data = %q, want %q", got, "hello world")
	}
}

// TestItemOperationsFetchBadReference proves a FileReference that names no
// attachment the caller holds is refused with status 15, MS-ASCMD's "Attachment
// fetch provider - Attachment or attachment ID is invalid", carries no data, and
// still echoes the reference. The reference arrives from the client, so each
// case is a forged or broken one: no separators, a non-numeric collection or
// message id, a message id past uint32, an empty segment, extra colons, a
// negative index, an index past the last attachment, and a message that does
// not exist.
func TestItemOperationsFetchBadReference(t *testing.T) {
	ts, dir := seededServer(t)
	seedInboxAttachment(t, dir)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	_, sync := postCommand(t, ts, "Sync", syncReq("1", ""))
	valid := respColl(t, sync).Child(wbxml.ASCommands).Child(wbxml.ASAdd).
		Child(wbxml.ASData).Child(wbxml.ABAttachments).Child(wbxml.ABAttachment).ChildText(wbxml.ABFileReference)
	coll, uid, _, ok := parseFileRef(valid)
	if !ok {
		t.Fatalf("Sync exposed an unreadable FileReference %q", valid)
	}

	for _, ref := range []string{
		"not-a-valid-ref",
		"inbox:" + uid + ":0",
		coll + ":x:0",
		coll + ":4294967296:0",
		":" + uid + ":0",
		coll + "::0",
		coll + ":" + uid + ":0:9",
		coll + ":" + uid + ":-1",
		coll + ":" + uid + ":1",
		coll + ":999999:0",
	} {
		req := wbxml.Elem(wbxml.IOItemOperations,
			wbxml.Elem(wbxml.IOFetch, wbxml.Str(wbxml.ABFileReference, ref)))
		_, root := postCommand(t, ts, "ItemOperations", req)
		fetch := root.Child(wbxml.IOResponse).Child(wbxml.IOFetch)
		if s := fetch.ChildText(wbxml.IOStatus); s != "15" {
			t.Errorf("Fetch(%q) Status = %q, want 15", ref, s)
		}
		if fetch.Child(wbxml.IOProperties) != nil {
			t.Errorf("Fetch(%q) returned data for an invalid reference", ref)
		}
		if fetch.ChildText(wbxml.ABFileReference) != ref {
			t.Errorf("Fetch(%q) did not echo the FileReference", ref)
		}
	}
}
