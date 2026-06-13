package webmail

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"hermex/internal/mime"
)

// uploadFile is one file part for a multipart compose POST in a test.
type uploadFile struct {
	field       string
	filename    string
	contentType string
	data        []byte
}

// multipartCompose builds a multipart/form-data compose body with the given text
// fields and file parts, returning the body and its Content-Type.
func multipartCompose(t *testing.T, fields map[string]string, files []uploadFile) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.field, f.filename))
		if f.contentType != "" {
			h.Set("Content-Type", f.contentType)
		}
		pw, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		pw.Write(f.data)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// findPart returns the first leaf part for which match returns true.
func findPart(p *mime.Part, match func(*mime.Part) bool) *mime.Part {
	if match(p) {
		return p
	}
	for _, c := range p.Children {
		if got := findPart(c, match); got != nil {
			return got
		}
	}
	return nil
}

// TestComposeUploadAttachment checks that a multipart compose with an uploaded
// file produces a multipart/mixed Sent copy carrying the file as an attachment
// (correct filename and content) alongside the text body.
func TestComposeUploadAttachment(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	body, ctype := multipartCompose(t,
		map[string]string{
			"action":  "send",
			"to":      "alice@hermex.test",
			"subject": "with file",
			"body":    "see attached",
		},
		[]uploadFile{{field: "attach", filename: "notes.txt", contentType: "text/plain", data: []byte("file contents here")}},
	)
	resp, err := c.Post(ts.URL+"/compose", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	raw := folderRaw(t, path, "Sent")
	root := mime.ParseStructure([]byte(raw))
	if root.Type != "multipart" || root.Subtype != "mixed" {
		t.Fatalf("Sent copy is %s/%s, want multipart/mixed:\n%s", root.Type, root.Subtype, raw)
	}
	// The body part carries the text.
	textPart := findPart(root, func(p *mime.Part) bool { return p.Type == "text" && p.Subtype == "plain" })
	if textPart == nil {
		t.Fatalf("no text/plain body part:\n%s", raw)
	}
	if tc, _ := textPart.DecodedContent(); !strings.Contains(string(tc), "see attached") {
		t.Errorf("body part lost its text = %q", tc)
	}
	// The uploaded file is an attachment with the right name and content.
	filePart := findPart(root, func(p *mime.Part) bool { return p.Filename() == "notes.txt" })
	if filePart == nil {
		t.Fatalf("uploaded file not present as an attachment:\n%s", raw)
	}
	if fc, _ := filePart.DecodedContent(); string(fc) != "file contents here" {
		t.Errorf("attachment content = %q, want %q", fc, "file contents here")
	}
}

// TestComposeUrlEncodedStillWorks guards the regression surface: a plain
// url-encoded compose (no upload, the autosave/no-JS path) still sends and files
// a Sent copy unaffected by the multipart-upload handling.
func TestComposeUrlEncodedStillWorks(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if code, _ := postForm(t, c, ts.URL+"/compose", map[string][]string{
		"action":  {"send"},
		"to":      {"alice@hermex.test"},
		"subject": {"no attachment"},
		"body":    {"plain send"},
	}); code != 200 {
		t.Fatalf("url-encoded send = %d", code)
	}
	raw := folderRaw(t, path, "Sent")
	if !strings.Contains(raw, "plain send") {
		t.Errorf("url-encoded send lost its body:\n%s", raw)
	}
	if strings.Contains(raw, "multipart/mixed") {
		t.Errorf("a no-attachment compose should not be multipart/mixed:\n%s", raw)
	}
}

// TestDraftAttachmentSurvivesReopenResaveAndSend checks the load-bearing draft
// round-trip: a file saved into a draft is listed when the draft reopens,
// survives a url-encoded re-save (the autosave path, which carries no file
// bytes), and rides along when the draft is finally sent. Without the submit-path
// re-read, the file would be silently dropped on the first re-save.
func TestDraftAttachmentSurvivesReopenResaveAndSend(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Save a draft WITH an uploaded file.
	body, ctype := multipartCompose(t,
		map[string]string{"action": "savedraft", "subject": "draft with file", "body": "draft body"},
		[]uploadFile{{field: "attach", filename: "doc.txt", contentType: "text/plain", data: []byte("attached draft data")}},
	)
	resp, err := c.Post(ts.URL+"/compose", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	drafts := folderMsgs(t, path, draftFID)
	if len(drafts) != 1 {
		t.Fatalf("Drafts has %d, want 1", len(drafts))
	}
	u1 := drafts[0].UID

	// Reopen: the editdraft page lists the existing attachment.
	if _, page := get(t, c, ts.URL+"/compose?action=editdraft&folder=Drafts&uid="+itoa(u1)); !strings.Contains(page, "doc.txt") {
		t.Errorf("editdraft did not list the existing attachment:\n%s", page)
	}

	// Re-save url-encoded (no file bytes, as autosave does), carrying the draft uid.
	postForm(t, c, ts.URL+"/compose", map[string][]string{
		"action": {"savedraft"}, "draftuid": {itoa(u1)}, "draftfolder": {"Drafts"},
		"subject": {"draft with file"}, "body": {"draft body edited"},
	})
	drafts = folderMsgs(t, path, draftFID)
	if len(drafts) != 1 {
		t.Fatalf("after re-save Drafts has %d, want 1", len(drafts))
	}
	u2 := drafts[0].UID
	raw := msgRaw(t, path, draftFID, u2)
	fp := findPart(mime.ParseStructure([]byte(raw)), func(p *mime.Part) bool { return p.Filename() == "doc.txt" })
	if fp == nil {
		t.Fatalf("re-saved draft LOST its attachment (the round-trip bug):\n%s", raw)
	}
	if fc, _ := fp.DecodedContent(); string(fc) != "attached draft data" {
		t.Errorf("re-saved attachment content = %q, want %q", fc, "attached draft data")
	}

	// Send the reopened draft (url-encoded): the attachment rides along to Sent,
	// and the draft is cleared.
	postForm(t, c, ts.URL+"/compose", map[string][]string{
		"action": {"send"}, "draftuid": {itoa(u2)}, "draftfolder": {"Drafts"},
		"to": {"alice@hermex.test"}, "subject": {"draft with file"}, "body": {"final"},
	})
	sent := folderRaw(t, path, "Sent")
	if findPart(mime.ParseStructure([]byte(sent)), func(p *mime.Part) bool { return p.Filename() == "doc.txt" }) == nil {
		t.Errorf("sent-from-draft lost the attachment:\n%s", sent)
	}
	if n := len(folderMsgs(t, path, draftFID)); n != 0 {
		t.Errorf("a sent draft must be removed from Drafts, found %d", n)
	}
}
