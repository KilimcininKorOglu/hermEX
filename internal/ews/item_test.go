package ews

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// seededWithMessage builds an EWS server over a mailbox seeded with the given
// raw messages appended to the Inbox, returning the server and the mailbox dir
// (so a test can mutate the store directly).
func seededWithMessage(t *testing.T, raws ...string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, raw := range raws {
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0); err != nil {
			st.Close()
			t.Fatalf("append: %v", err)
		}
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

var (
	itemIDRE   = regexp.MustCompile(`<(?:\w+:)?ItemId Id="([^"]+)"`)
	attachIDRE = regexp.MustCompile(`<(?:\w+:)?AttachmentId Id="([^"]+)"`)
)

func findItemReq(parent string) string {
	return wrapRequest(`<FindItem Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>Default</BaseShape></ItemShape>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="` + parent + `" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindItem>`)
}

func getItemReq(itemID string) string {
	return wrapRequest(`<GetItem xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>AllProperties</BaseShape></ItemShape>` +
		`<ItemIds><t:ItemId Id="` + itemID + `" xmlns:t="` + nsTypes + `"/></ItemIds>` +
		`</GetItem>`)
}

func getAttachmentReq(attachID string) string {
	return wrapRequest(`<GetAttachment xmlns="` + nsMessages + `">` +
		`<AttachmentIds><t:AttachmentId Id="` + attachID + `" xmlns:t="` + nsTypes + `"/></AttachmentIds>` +
		`</GetAttachment>`)
}

const plainMessage = "From: Bob <bob@hermex.test>\r\n" +
	"To: Alice <alice@hermex.test>\r\n" +
	"Subject: Hello EWS\r\n" +
	"Date: Wed, 12 Jun 2024 13:46:40 +0000\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
	"the body text\r\n"

const attachMessage = "From: Bob <bob@hermex.test>\r\n" +
	"To: Alice <alice@hermex.test>\r\n" +
	"Subject: With attachment\r\n" +
	"Date: Wed, 12 Jun 2024 13:46:40 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=BB\r\n\r\n" +
	"--BB\r\n" +
	"Content-Type: text/plain\r\n\r\n" +
	"see attached\r\n" +
	"--BB\r\n" +
	"Content-Type: application/octet-stream\r\n" +
	"Content-Disposition: attachment; filename=\"doc.txt\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\n" +
	"ZmlsZSBkYXRh\r\n" +
	"--BB--\r\n"

// TestFindItemAndGetItem confirms FindItem lists a message and GetItem returns
// its body and metadata.
func TestFindItemAndGetItem(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)

	resp, out := soapPost(t, ts, findItemReq("inbox"), true)
	if resp.StatusCode != 200 {
		t.Fatalf("FindItem status = %d: %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Hello EWS") {
		t.Errorf("FindItem missing subject: %s", out)
	}
	itemID := itemIDRE.FindStringSubmatch(out)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", out)
	}

	_, out2 := soapPost(t, ts, getItemReq(itemID[1]), true)
	if !strings.Contains(out2, `ResponseClass="Success"`) {
		t.Errorf("GetItem not success: %s", out2)
	}
	if !strings.Contains(out2, "the body text") {
		t.Errorf("GetItem missing body: %s", out2)
	}
	if !strings.Contains(out2, "bob@hermex.test") {
		t.Errorf("GetItem missing From address: %s", out2)
	}
}

// TestGetAttachment confirms GetItem lists an attachment and GetAttachment
// returns its base64 content.
func TestGetAttachment(t *testing.T) {
	ts, _ := seededWithMessage(t, attachMessage)

	_, out := soapPost(t, ts, findItemReq("inbox"), true)
	itemID := itemIDRE.FindStringSubmatch(out)
	if len(itemID) != 2 {
		t.Fatalf("no ItemId: %s", out)
	}
	_, gi := soapPost(t, ts, getItemReq(itemID[1]), true)
	if !strings.Contains(gi, "doc.txt") {
		t.Errorf("GetItem missing attachment name: %s", gi)
	}
	attachID := attachIDRE.FindStringSubmatch(gi)
	if len(attachID) != 2 {
		t.Fatalf("GetItem returned no AttachmentId: %s", gi)
	}

	_, ga := soapPost(t, ts, getAttachmentReq(attachID[1]), true)
	if !strings.Contains(ga, `ResponseClass="Success"`) {
		t.Errorf("GetAttachment not success: %s", ga)
	}
	// base64("file data") == "ZmlsZSBkYXRh"
	if !strings.Contains(ga, "ZmlsZSBkYXRh") {
		t.Errorf("GetAttachment missing content: %s", ga)
	}
}
