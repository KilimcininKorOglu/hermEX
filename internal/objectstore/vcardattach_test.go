package objectstore

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxvcard"
)

// TestReceivedVCardIsAnAttachedContact proves a received vCard is stored as an
// attached contact ([MS-OXCMAIL] 2.2.3.4.4): an embedded message whose contact
// properties PrEmbeddedContact keeps, both for a vCard 2.1 file part and for a
// vCard that is the alternative body of a message, which is then not the body.
func TestReceivedVCardIsAnAttachedContact(t *testing.T) {
	for _, tc := range []struct {
		name, raw, wantBody string
	}{
		{"x-vcard 2.1 file", "From: a@example.test\r\nTo: b@example.test\r\nSubject: Card\r\n" +
			"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n" +
			"--m\r\nContent-Type: text/plain\r\n\r\nMy card.\r\n" +
			"--m\r\nContent-Type: text/x-vcard; name=\"ayse.vcf\"\r\nContent-Disposition: attachment; filename=\"ayse.vcf\"\r\n\r\n" +
			"BEGIN:VCARD\r\nVERSION:2.1\r\nN:Gul;Ayse\r\nFN:Ayse Gul\r\nTEL;CELL:+90 532 555 0102\r\nEND:VCARD\r\n" +
			"--m--\r\n", "My card."},
		{"alternative 3.0", "From: a@example.test\r\nTo: b@example.test\r\nSubject: Card\r\n" +
			"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
			"--a\r\nContent-Type: text/plain\r\n\r\nMy card.\r\n" +
			"--a\r\nContent-Type: text/directory; profile=vCard; charset=utf-8\r\n\r\n" +
			"BEGIN:VCARD\r\nVERSION:3.0\r\nN:Gul;Ayse;;;\r\nFN:Ayse Gul\r\nTEL;TYPE=CELL:+90 532 555 0102\r\nEND:VCARD\r\n" +
			"--a--\r\n", "My card."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openSeededStore(t)
			info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(tc.raw), time.Now(), 0)
			if err != nil {
				t.Fatal(err)
			}
			msg, err := st.OpenMessage(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if body, _ := msg.Props.Get(mapi.PrBody); body != tc.wantBody+"\r\n" && body != tc.wantBody {
				t.Errorf("body = %q, want %q (the vCard is not the body)", body, tc.wantBody)
			}
			if len(msg.Attachments) != 1 {
				t.Fatalf("%d attachments, want the one contact", len(msg.Attachments))
			}
			wantAttachedContact(t, st, msg.Attachments[0].Props)
		})
	}
}

// wantAttachedContact checks an attachment is the contact the test card describes.
func wantAttachedContact(t *testing.T, st *Store, att mapi.PropertyValues) {
	t.Helper()
	if m, _ := att.Get(mapi.PrAttachMethod); m != int32(mapi.AttachEmbeddedMsg) {
		t.Errorf("attach method = %v, want an embedded message", m)
	}
	blob, _ := att.Get(mapi.PrEmbeddedContact)
	raw, _ := blob.([]byte)
	contact, err := oxvcard.RestoreEmbedded(raw, st.GetNamedPropIDs)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := contact.Get(mapi.PrMobileTelephoneNumber); got != "+90 532 555 0102" {
		t.Errorf("contact mobile = %q", got)
	}
	if got, _ := contact.Get(mapi.PrMessageClass); got != "IPM.Contact" {
		t.Errorf("contact class = %q", got)
	}
}

// TestReceivedVCardIsServedAsAVCard proves the stored contact goes back out as the
// vCard 3.0 part [MS-OXCMAIL] 2.1.3.4.6 calls for.
func TestReceivedVCardIsServedAsAVCard(t *testing.T) {
	st := openSeededStore(t)
	raw := "From: a@example.test\r\nTo: b@example.test\r\nSubject: Card\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/x-vcard\r\n\r\n" +
		"BEGIN:VCARD\r\nVERSION:2.1\r\nN:Gul;Ayse\r\nFN:Ayse Gul\r\nEND:VCARD\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	served, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), info.UID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"text/directory", "VERSION:3.0", "FN:Ayse Gul"} {
		if !bytes.Contains(served, []byte(want)) {
			t.Errorf("served message lacks %q:\n%s", want, served)
		}
	}
}
