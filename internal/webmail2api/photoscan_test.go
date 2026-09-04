package webmail2api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/avtest"
	"hermex/internal/mapi"
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

// contactHasPhoto reports whether the contact carries a photo attachment.
func contactHasPhoto(t *testing.T, mbox, id string) bool {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListFolderObjects(int64(mapi.PrivateFIDContacts))
	if err != nil {
		t.Fatal(err)
	}
	for _, mid := range msgs {
		msg, err := st.OpenMessage(mid.ID)
		if err != nil {
			continue
		}
		if _, _, has := contactPhotoAttachment(msg); has {
			return true
		}
	}
	return false
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
