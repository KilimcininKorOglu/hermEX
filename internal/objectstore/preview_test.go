package objectstore

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// listOne appends a raw message to the Inbox and returns its index row.
func listOne(t *testing.T, raw string) MessageInfo {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	return msgs[0]
}

const plainMail = "From: alice@hermex.test\r\n" +
	"To: bob@hermex.test\r\n" +
	"Subject: quarterly\r\n" +
	"Date: Mon, 2 Feb 2026 10:00:00 +0000\r\n" +
	"\r\n" +
	"The numbers are in.\r\nSecond line nobody sees in a list.\r\n"

// TestListRowCarriesAPreview is why the column exists: a message list renders a
// snippet under each subject, and reading every message's body to build one
// would make a page of rows cost a page of message reads.
func TestListRowCarriesAPreview(t *testing.T) {
	m := listOne(t, plainMail)
	if !strings.HasPrefix(m.Preview, "The numbers are in.") {
		t.Errorf("preview = %q", m.Preview)
	}
	// One line: the newline is collapsed rather than carried into the row.
	if strings.Contains(m.Preview, "\n") {
		t.Errorf("preview carries a line break: %q", m.Preview)
	}
}

// TestPreviewFallsBackToTheHtmlBody keeps a message that carries only HTML from
// listing with an empty snippet.
func TestPreviewFallsBackToTheHtmlBody(t *testing.T) {
	raw := "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: html only\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>p{color:red}</style></head><body><p>Hello &amp; welcome</p></body></html>\r\n"
	m := listOne(t, raw)
	if !strings.Contains(m.Preview, "Hello & welcome") {
		t.Errorf("preview = %q, want the text of the HTML body", m.Preview)
	}
	// Style and script text is markup, not the message.
	if strings.Contains(m.Preview, "color:red") {
		t.Errorf("preview carries stylesheet text: %q", m.Preview)
	}
}

// TestPreviewIsBounded keeps a long body from being stored twice: once as the
// message and once as an index row nothing renders.
func TestPreviewIsBounded(t *testing.T) {
	raw := "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: long\r\n\r\n" + strings.Repeat("word ", 500)
	m := listOne(t, raw)
	if got := len([]rune(m.Preview)); got != previewRunes {
		t.Errorf("preview is %d runes, want the cap of %d", got, previewRunes)
	}
}

// TestHasAttachmentsDrivesThePaperclip pins the flag the message list reads.
func TestHasAttachmentsDrivesThePaperclip(t *testing.T) {
	if m := listOne(t, plainMail); m.HasAttachments {
		t.Error("a message with no attachment is marked as carrying one")
	}

	withFile := "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: invoice\r\n" +
		"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"i.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--b1--\r\n"
	if m := listOne(t, withFile); !m.HasAttachments {
		t.Error("a message carrying a file is not marked as having an attachment")
	}
}

// TestInlinePictureIsNotAnAttachment is the distinction the paperclip depends
// on: a signature logo referenced by the HTML body would otherwise mark every
// message as carrying an attachment, and the icon would mean nothing.
func TestInlinePictureIsNotAnAttachment(t *testing.T) {
	raw := "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: signed off\r\n" +
		"Content-Type: multipart/related; boundary=r1\r\n\r\n" +
		"--r1\r\nContent-Type: text/html\r\n\r\n<p>hi <img src=\"cid:logo@hermex.test\"></p>\r\n" +
		"--r1\r\nContent-Type: image/png\r\nContent-ID: <logo@hermex.test>\r\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--r1--\r\n"
	if m := listOne(t, raw); m.HasAttachments {
		t.Error("an inline picture marked the message as carrying an attachment")
	}
}
