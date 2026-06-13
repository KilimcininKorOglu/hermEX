package webmail

import (
	"net/http"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestImportFormListsFolders checks the import page offers a destination folder
// picker and a file field.
func TestImportFormListsFolders(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, page := get(t, c, ts.URL+"/import")
	if code != 200 {
		t.Fatalf("GET /import = %d", code)
	}
	if !strings.Contains(page, `name="eml"`) || !strings.Contains(page, `name="folder"`) {
		t.Errorf("import form missing the file or folder field:\n%s", page)
	}
}

// TestImportEmlIntoFolder checks that an uploaded .eml is stored in the chosen
// folder and round-trips its subject and body through the store.
func TestImportEmlIntoFolder(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	eml := "From: Carol <carol@example.com>\r\n" +
		"To: alice@hermex.test\r\n" +
		"Subject: imported message\r\n\r\n" +
		"this is the imported body\r\n"
	body, ctype := multipartCompose(t,
		map[string]string{"folder": "INBOX"},
		[]uploadFile{{field: "eml", filename: "saved.eml", contentType: "message/rfc822", data: []byte(eml)}},
	)
	resp, err := c.Post(ts.URL+"/import", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 { // 303 to /mail, followed by the client
		t.Fatalf("import status = %d, want 200 after redirect", resp.StatusCode)
	}

	inbox := folderMsgs(t, path, int64(mapi.PrivateFIDInbox))
	if len(inbox) != 1 {
		t.Fatalf("INBOX has %d messages after import, want 1", len(inbox))
	}
	raw := msgRaw(t, path, int64(mapi.PrivateFIDInbox), inbox[0].UID)
	if !strings.Contains(raw, "imported message") {
		t.Errorf("imported message lost its subject:\n%s", raw)
	}
	if !strings.Contains(raw, "this is the imported body") {
		t.Errorf("imported message lost its body:\n%s", raw)
	}
}

// TestImportRejectsMalformed checks that content which is not a parseable email
// is refused with 400 and stored nowhere — oxcmail import is lenient, so the
// handler must guard against filing arbitrary bytes as an empty note.
func TestImportRejectsMalformed(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	body, ctype := multipartCompose(t,
		map[string]string{"folder": "INBOX"},
		[]uploadFile{{field: "eml", filename: "junk.bin", contentType: "application/octet-stream", data: []byte("this is not an email at all")}},
	)
	resp, err := c.Post(ts.URL+"/import", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed import status = %d, want 400", resp.StatusCode)
	}
	if n := len(folderMsgs(t, path, int64(mapi.PrivateFIDInbox))); n != 0 {
		t.Errorf("a rejected import must store nothing, INBOX has %d", n)
	}
}
