package webmail2api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/avtest"
	"hermex/internal/objectstore"
)

// createContact adds one contact and returns its id.
func createContact(t *testing.T, srv *Server, token string) string {
	t.Helper()
	body, err := json.Marshal(contactJSON{Name: "Bob", Email: "bob@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/contacts", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create contact = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Contact contactJSON `json:"contact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Contact.ID
}

// putPhoto uploads photo bytes for a contact.
func putPhoto(t *testing.T, srv *Server, token, id string, photo []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(photo); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/contacts/"+id+"/photo", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// contactHasPhoto reports whether the NAMED contact carries a photo attachment.
// It opens that one contact rather than scanning the folder, because a scan
// answers "some contact has a photo" and passes on a mailbox holding a single
// contact even when the upload landed on the wrong object.
func contactHasPhoto(t *testing.T, mbox, id string) bool {
	t.Helper()
	mid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("contact id %q is not a message id: %v", id, err)
	}
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msg, err := st.OpenMessage(mid)
	if err != nil {
		t.Fatalf("open contact %d: %v", mid, err)
	}
	num, data, has := objectstore.ContactPhotoAttachment(msg)
	if has && (num < 0 || len(data) == 0) {
		t.Fatalf("contact %d marks photo attachment %d with %d bytes", mid, num, len(data))
	}
	return has
}

// TestContactPhotoRefusesInfectedContent is the missing-scan defect. The uploaded
// bytes are written straight into the mailbox as an attachment and never pass
// through delivery, so they are scanned here or not at all. Every sibling path that
// stores client-supplied attachment content already scans; this one did not.
func TestContactPhotoRefusesInfectedContent(t *testing.T) {
	withScanner(t, avtest.Infected)
	srv, token, mbox := importHarness(t)
	id := createContact(t, srv, token)

	rec := putPhoto(t, srv, token, id, []byte("MZ malware bytes"))
	if rec.Code == http.StatusOK {
		t.Errorf("infected photo was accepted: %s", rec.Body.String())
	}
	if contactHasPhoto(t, mbox, id) {
		t.Error("the infected photo was stored on the contact anyway")
	}
}

// TestARefusedPhotoKeepsThePreviousOne: an infected or empty upload is refused
// and the contact keeps the clean photo it already had, byte for byte.
func TestARefusedPhotoKeepsThePreviousOne(t *testing.T) {
	withScanner(t, avtest.Clean)
	srv, token, mbox := importHarness(t)
	id := createContact(t, srv, token)
	clean := []byte("an ordinary photo")
	wantStatus(t, "clean photo", putPhoto(t, srv, token, id, clean), http.StatusOK)

	if rec := putPhoto(t, srv, token, id, nil); rec.Code == http.StatusOK {
		t.Errorf("an empty photo was accepted: %s", rec.Body.String())
	}
	withScanner(t, avtest.Infected)
	if rec := putPhoto(t, srv, token, id, []byte("MZ malware bytes")); rec.Code == http.StatusOK {
		t.Errorf("an infected photo was accepted: %s", rec.Body.String())
	}

	mid, err := strconv.ParseInt(id, 10, 64)
	mustNoErr(t, "contact id", err)
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	msg, err := st.OpenMessage(mid)
	mustNoErr(t, "open the contact", err)
	num, data, has := objectstore.ContactPhotoAttachment(msg)
	wantEq(t, "photo kept", has, true)
	if num < 0 || !bytes.Equal(data, clean) {
		t.Errorf("photo attachment %d holds %q, want the earlier clean photo", num, data)
	}
}

// TestContactPhotoAcceptsCleanContent keeps the ordinary upload working.
func TestContactPhotoAcceptsCleanContent(t *testing.T) {
	withScanner(t, avtest.Clean)
	srv, token, mbox := importHarness(t)
	id := createContact(t, srv, token)

	rec := putPhoto(t, srv, token, id, []byte("an ordinary photo"))
	if rec.Code != http.StatusOK {
		t.Fatalf("clean photo status = %d: %s", rec.Code, rec.Body.String())
	}
	if !contactHasPhoto(t, mbox, id) {
		t.Error("the clean photo was not stored")
	}
}
