package activesync

import (
	"encoding/base64"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// seedAddressedContact stores one contact carrying a display name and one e-mail
// address, and returns its message id.
func seedAddressedContact(t *testing.T, dir, name, addr string) int64 {
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

// seedContactPhoto hangs a photo attachment on a contact, the shape webmail
// stores when the user uploads a contact picture.
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

// TestResolveRecipientsFindsAnOwnContact is the load-bearing case: the address a
// user corresponds with is often saved in their own address book and nowhere
// else, and that is the address the client asks to resolve.
func TestResolveRecipientsFindsAnOwnContact(t *testing.T) {
	ts, dir := seededServer(t)
	seedAddressedContact(t, dir, "Ada Lovelace", "ada@partner.example")

	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq("Lovelace"))

	resp := responseFor(root, "Lovelace")
	if resp == nil {
		t.Fatal("no Response echoed for the query")
	}
	if s := resp.ChildText(wbxml.RRStatus); s != "1" {
		t.Fatalf("response Status = %q, want 1 (resolved)", s)
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("the contact resolved to no Recipient")
	}
	if got := rec.ChildText(wbxml.RREmailAddress); got != "ada@partner.example" {
		t.Errorf("Recipient address = %q, want the contact's", got)
	}
	// [MS-ASCMD] 2.2.3.186.5: 2 is a contact entry, 1 is a GAL entry.
	if got := rec.ChildText(wbxml.RRType); got != "2" {
		t.Errorf("Recipient Type = %q, want 2 (contact entry)", got)
	}
}

// TestResolveRecipientsServesAContactPicture proves the picture on the contact
// card reaches the client, the same picture webmail shows for that address.
func TestResolveRecipientsServesAContactPicture(t *testing.T) {
	ts, dir := seededServer(t)
	photo := []byte("\xff\xd8\xff\xe0 contact jpeg")
	seedContactPhoto(t, dir, seedAddressedContact(t, dir, "Ada Lovelace", "ada@partner.example"), photo)

	_, root := postCommand(t, ts, "ResolveRecipients", pictureRequest("Lovelace"))

	resp := responseFor(root, "Lovelace")
	if resp == nil {
		t.Fatal("no Response echoed for the query")
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("the contact resolved to no Recipient")
	}
	pic := rec.Child(wbxml.RRPicture)
	if pic == nil {
		t.Fatal("Recipient carried no Picture")
	}
	if s := pic.ChildText(wbxml.RRStatus); s != "1" {
		t.Fatalf("Picture Status = %q, want 1", s)
	}
	if want := base64.StdEncoding.EncodeToString(photo); pic.ChildText(wbxml.RRData) != want {
		t.Errorf("Picture Data is not the contact photo")
	}
}

// TestResolveRecipientsReportsOneAddressOnce keeps a person who is both a
// colleague and a saved contact from resolving twice.
func TestResolveRecipientsReportsOneAddressOnce(t *testing.T) {
	ts, dir := seededServer(t)
	seedAddressedContact(t, dir, "Alice Saved", testUser) // the address the GAL already answers

	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq("alice"))

	resp := responseFor(root, "alice")
	if resp == nil {
		t.Fatal("no Response echoed for the query")
	}
	if c := resp.ChildText(wbxml.RRRecipientCount); c != "1" {
		t.Errorf("RecipientCount = %q, want 1 (the address is reported once)", c)
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("the query resolved to no Recipient")
	}
	if got := rec.ChildText(wbxml.RRType); got != "1" {
		t.Errorf("Recipient Type = %q, want 1: the address book answers first", got)
	}
}
