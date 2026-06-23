package webmail

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestDownloadAllAttachmentsZip checks that GET /attachments streams every
// displayed attachment of a message as a zip, with the right names and content,
// and leaves the text body part out.
func TestDownloadAllAttachmentsZip(t *testing.T) {
	path := emptyMailbox(t)
	raw := "From: a@b\r\nTo: c@d\r\nSubject: two files\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nbody text\r\n" +
		"--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"a.pdf\"\r\n\r\nPDF-A\r\n" +
		"--B\r\nContent-Type: text/csv\r\nContent-Disposition: attachment; filename=\"b.csv\"\r\n\r\nx,y,z\r\n--B--\r\n"
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(100, 0), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	ts := newTestServer(t, path)
	cl := authedClient(t, ts)
	resp, err := cl.Get(ts.URL + "/attachments?folder=INBOX&uid=" + itoa(info.UID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("zip status %d, want 200", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("response is not a valid zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if got["a.pdf"] != "PDF-A" {
		t.Errorf("a.pdf = %q, want PDF-A (zip entries: %v)", got["a.pdf"], got)
	}
	if got["b.csv"] != "x,y,z" {
		t.Errorf("b.csv = %q, want x,y,z", got["b.csv"])
	}
	if len(got) != 2 {
		t.Errorf("zip has %d entries, want exactly the 2 attachments (no body part): %v", len(got), got)
	}
}
