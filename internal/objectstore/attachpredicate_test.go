package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// paperclipAnswers appends a raw message and returns the two answers to "does this
// message carry an attachment": the index row's has_attach column (webmail reads it)
// and the Store.HasAttachments query (EWS asks it).
func paperclipAnswers(t *testing.T, raw string) (indexed, queried bool) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	queried, err = st.HasAttachments(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return info.HasAttachments, queried
}

// attachmentWithContentID is the case the two predicates used to disagree on: a real
// PDF attachment that also carries a Content-ID header. Senders stamp one on every
// part, and the part is still an attachment because its disposition says so.
const attachmentWithContentID = "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: invoice\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n" +
	"--b1\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
	"--b1\r\nContent-Type: application/pdf\r\nContent-ID: <part1@hermex.test>\r\n" +
	"Content-Disposition: attachment; filename=\"i.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--b1--\r\n"

// inlineImageWithoutContentID pins the approximation both predicates share: an
// inline image counts as inline from its disposition and media type alone, so it is
// excluded even though the body carries no cid: reference to it.
const inlineImageWithoutContentID = "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: picture\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=r1\r\n\r\n" +
	"--r1\r\nContent-Type: text/html\r\n\r\n<p>hi</p>\r\n" +
	"--r1\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=\"p.png\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--r1--\r\n"

// referencedInlineImage is the case both predicates always agreed on: a signature
// logo the HTML body renders in place.
const referencedInlineImage = "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: signed off\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=r1\r\n\r\n" +
	"--r1\r\nContent-Type: text/html\r\n\r\n<p>hi <img src=\"cid:logo@hermex.test\"></p>\r\n" +
	"--r1\r\nContent-Type: image/png\r\nContent-ID: <logo@hermex.test>\r\n" +
	"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--r1--\r\n"

// TestDumpsterRowCarriesThePaperclip proves the mailbox-wide dumpster listing reports
// the attachment too. ListAllSoftDeleted builds its MessageInfo by hand from the
// opened message, so a field it forgets reads as a fact to the caller: the EWS
// recoverableitemsdeletions folder would show a message with an attachment as
// carrying none.
func TestDumpsterRowCarriesThePaperclip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(attachmentWithContentID), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), info.UID); err != nil {
		t.Fatal(err)
	}
	items, err := st.ListAllSoftDeleted()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("dumpster holds %d items, want 1", len(items))
	}
	if !items[0].Info.HasAttachments {
		t.Error("the dumpster row reports no attachment for a message that carries one")
	}
	if items[0].Info.Preview == "" {
		t.Error("the dumpster row carries no preview")
	}
}

// TestThePaperclipIsOneAnswer is the load-bearing case: whether a message carries an
// attachment is ONE fact, and the index column and the store query must answer it
// alike. Two predicates let EWS and webmail show a different paperclip for the same
// message in the same mailbox.
func TestThePaperclipIsOneAnswer(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"a plain message carries none", plainMail, false},
		{"a PDF with a Content-ID is still an attachment", attachmentWithContentID, true},
		{"an inline image is not an attachment", inlineImageWithoutContentID, false},
		{"a referenced inline picture is not an attachment", referencedInlineImage, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			indexed, queried := paperclipAnswers(t, c.raw)
			if indexed != queried {
				t.Errorf("the index column says %v and the store query says %v; the two must agree", indexed, queried)
			}
			if indexed != c.want {
				t.Errorf("index column = %v, want %v", indexed, c.want)
			}
			if queried != c.want {
				t.Errorf("store query = %v, want %v", queried, c.want)
			}
		})
	}
}
