package webmail2api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// threeAttachments is a message carrying three files, so an archive narrowed to
// a subset is distinguishable from the whole.
const threeAttachments = "From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: files\r\n" +
	"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
	"--b1\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
	"--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"first.pdf\"\r\n\r\nONE\r\n" +
	"--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"second.pdf\"\r\n\r\nTWO\r\n" +
	"--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"third.pdf\"\r\n\r\nTHREE\r\n--b1--\r\n"

// zipHarness seeds the message and returns a request helper for the archive.
func zipHarness(t *testing.T) func(query string) *httptest.ResponseRecorder {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(threeAttachments), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("zip-test-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	id := messageID("inbox", info.UID)
	return func(query string) *httptest.ResponseRecorder {
		token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		target := "/api/v1/mail/attachments-zip?id=" + id + query
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
}

// zipNames lists the entries of a returned archive.
func zipNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

// TestAttachmentsZipHoldsEverythingByDefault keeps the call that takes no index
// answering exactly as it did.
func TestAttachmentsZipHoldsEverythingByDefault(t *testing.T) {
	do := zipHarness(t)
	got := zipNames(t, do(""))
	want := []string{"first.pdf", "second.pdf", "third.pdf"}
	if len(got) != len(want) {
		t.Fatalf("archive holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("archive holds %v, want %v", got, want)
			break
		}
	}
}

// TestAttachmentsZipNarrowsToTheChosenIndexes is the feature: a selection is
// handed to the desktop as one archive of exactly that selection.
func TestAttachmentsZipNarrowsToTheChosenIndexes(t *testing.T) {
	do := zipHarness(t)
	got := zipNames(t, do("&index=0&index=2"))
	if len(got) != 2 || got[0] != "first.pdf" || got[1] != "third.pdf" {
		t.Errorf("narrowed archive holds %v, want [first.pdf third.pdf]", got)
	}
}

// TestAttachmentsZipDropsARepeatedIndex keeps one file from appearing twice
// under one name, which no archive reader shows sensibly.
func TestAttachmentsZipDropsARepeatedIndex(t *testing.T) {
	do := zipHarness(t)
	if got := zipNames(t, do("&index=1&index=1")); len(got) != 1 {
		t.Errorf("archive holds %v, want one entry", got)
	}
}

// TestAttachmentsZipRefusesAnIndexTheMessageDoesNotHold is why the check runs
// before a byte is written. The response streams, so an index rejected halfway
// would already have been answered 200, and the reader would receive a short
// archive that looks complete.
func TestAttachmentsZipRefusesAnIndexTheMessageDoesNotHold(t *testing.T) {
	do := zipHarness(t)
	for _, q := range []string{"&index=3", "&index=-1", "&index=abc", "&index=0&index=9"} {
		if rec := do(q); rec.Code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", q, rec.Code)
		}
	}
}
