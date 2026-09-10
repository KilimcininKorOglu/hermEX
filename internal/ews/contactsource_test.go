package ews

import (
	"encoding/base64"
	"encoding/xml"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// seedContact stores one contact in a mailbox's Contacts folder and returns its
// message id, so a test can hang a photo on it.
func seedContact(t *testing.T, dir, name, addr string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameEmail1Address})
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G115 -- a named-property id is a 16-bit value shifted into the tag's high half
	emailTag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode))
	id, err := st.CreateMessage(int64(mapi.PrivateFIDContacts), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: name},
		{Tag: emailTag, Value: addr},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedContactPhoto hangs a contact photo attachment on a contact, the shape
// webmail stores when the user uploads a picture.
func seedContactPhoto(t *testing.T, dir string, id int64, data []byte) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.CreateAttachment(id, mapi.PropertyValues{
		{Tag: mapi.PrAttachDataBin, Value: data},
		{Tag: mapi.PrAttachFilename, Value: "ContactPicture.jpg"},
		{Tag: mapi.PrAttachmentContactPhoto, Value: true},
	}); err != nil {
		t.Fatal(err)
	}
}

// getUserPhotoBody builds a GetUserPhoto request for one address.
func getUserPhotoBody(addr string) string {
	return wrapRequest(`<GetUserPhoto xmlns="` + nsMessages + `"><Email>` + addr +
		`</Email><SizeRequested>HR648x648</SizeRequested></GetUserPhoto>`)
}

// TestGetUserPhotoServesAContactPhoto is the load-bearing case: the picture of an
// external correspondent lives on the caller's own contact card, and it is the
// only picture there is for that address.
func TestGetUserPhotoServesAContactPhoto(t *testing.T) {
	ts, dir := seededEWS(t)
	photo := []byte("\xff\xd8\xff\xe0 contact jpeg")
	seedContactPhoto(t, dir, seedContact(t, dir, "Ada Lovelace", "ada@partner.example"), photo)

	_, out := soapPost(t, ts, getUserPhotoBody("ada@partner.example"), true)

	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("a saved contact's photo was not served: %s", out)
	}
	if want := base64.StdEncoding.EncodeToString(photo); !strings.Contains(out, want) {
		t.Errorf("PictureData does not carry the contact photo: %s", out)
	}
}

// TestGetUserPhotoPrefersTheMailboxPhoto proves the directory's own portrait wins:
// a contact card somebody saved for a colleague must not override the picture that
// colleague published.
func TestGetUserPhotoPrefersTheMailboxPhoto(t *testing.T) {
	ts, dir := seededEWS(t)
	mailboxPhoto := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserPhoto(mailboxPhoto); err != nil {
		t.Fatal(err)
	}
	st.Close()
	seedContactPhoto(t, dir, seedContact(t, dir, "Alice Saved", testUser), []byte("contact card jpeg"))

	_, out := soapPost(t, ts, getUserPhotoBody(testUser), true)

	if want := base64.StdEncoding.EncodeToString(mailboxPhoto); !strings.Contains(out, want) {
		t.Errorf("the mailbox portrait did not win over the contact card: %s", out)
	}
}

// TestGetUserPhotoWithoutAnySourceIsNotFound proves the fallback did not turn a
// missing photo into a success.
func TestGetUserPhotoWithoutAnySourceIsNotFound(t *testing.T) {
	ts, dir := seededEWS(t)
	seedContact(t, dir, "Ada Lovelace", "ada@partner.example") // a contact with no picture

	_, out := soapPost(t, ts, getUserPhotoBody("ada@partner.example"), true)

	if !strings.Contains(out, "ErrorItemNotFound") {
		t.Errorf("an address with no photo anywhere must be ErrorItemNotFound: %s", out)
	}
}

// TestGetPersonaFindsTheUsersOwnContact closes the gap between the two operations:
// FindPeople offers a saved contact as a persona, so the client then asks
// GetPersona for that same address and must be answered.
func TestGetPersonaFindsTheUsersOwnContact(t *testing.T) {
	ts, dir := seededEWS(t)
	seedContact(t, dir, "Ada Lovelace", "ada@partner.example")

	_, body := soapPost(t, ts, wrapRequest(getPersonaBody("ada@partner.example")), true)

	var p parsedGetPersona
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetPersona: %v\n%s", err, body)
	}
	if p.Msg.Class != "Success" || p.Msg.Code != "NoError" {
		t.Fatalf("class/code = %q/%q, want Success/NoError\n%s", p.Msg.Class, p.Msg.Code, body)
	}
	assertPersona(t, p.Msg.Persona, "ada@partner.example")
	if p.Msg.Persona.DisplayName != "Ada Lovelace" {
		t.Errorf("display name = %q, want the contact's", p.Msg.Persona.DisplayName)
	}
}

// TestGetPersonaStillRefusesAStranger proves the contact fallback did not turn
// every address into a persona.
func TestGetPersonaStillRefusesAStranger(t *testing.T) {
	ts, dir := seededEWS(t)
	seedContact(t, dir, "Ada Lovelace", "ada@partner.example")

	_, body := soapPost(t, ts, wrapRequest(getPersonaBody("nobody@partner.example")), true)

	if !strings.Contains(body, "ErrorPersonNotFound") {
		t.Errorf("an address nobody saved must not resolve: %s", body)
	}
}
