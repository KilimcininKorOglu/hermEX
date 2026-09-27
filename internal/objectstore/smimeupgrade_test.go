package objectstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

const upgradeSignedMail = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Signed\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=\"s1\"\r\n\r\n" +
	"--s1\r\nContent-Type: text/plain\r\n\r\nsigned text\r\n" +
	"--s1\r\nContent-Type: application/pkcs7-signature; name=smime.p7s\r\nContent-Transfer-Encoding: base64\r\n\r\nMIIB\r\n--s1--\r\n"

// legacySigned files a signed message and puts it back in the shape delivery gave
// it before the S/MIME import: a plain note with its signature as a loose file.
func legacySigned(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(upgradeSignedMail), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.replaceAttachments(info.ID, nil); err != nil {
		t.Fatal(err)
	}
	attID, _, err := st.CreateAttachment(info.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAttachmentProperties(attID, mapi.PropertyValues{
		{Tag: mapi.PrAttachMimeTag, Value: "application/pkcs7-signature"},
		{Tag: mapi.PrAttachLongFilename, Value: "smime.p7s"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyMessageProperties(info.ID, mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: "IPM.Note"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, smimeUpgradedMarker)); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// TestOpenUpgradesAStoredSignedMessage proves a signed message filed before the
// S/MIME import takes the [MS-OXOSMIME] shape on the next open, in place, and
// that the pass records it is done.
func TestOpenUpgradesAStoredSignedMessage(t *testing.T) {
	dir := t.TempDir()
	id := legacySigned(t, dir)

	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msg, err := st.OpenMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if class, _ := msg.Props.Get(mapi.PrMessageClass); class != "IPM.Note.SMIME.MultipartSigned" {
		t.Errorf("class = %v", class)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want the one signed entity", len(msg.Attachments))
	}
	if tag, _ := msg.Attachments[0].Props.Get(mapi.PrAttachMimeTag); tag != "multipart/signed" {
		t.Errorf("attachment type = %v", tag)
	}
	if _, err := os.Stat(filepath.Join(dir, smimeUpgradedMarker)); err != nil {
		t.Errorf("the pass left no marker: %v", err)
	}
}

// TestUpgradeWaitsForAnIdleMailbox proves the conversion never runs while the
// mailbox is open elsewhere, and runs on a later open.
func TestUpgradeWaitsForAnIdleMailbox(t *testing.T) {
	dir := t.TempDir()
	id := legacySigned(t, dir)
	// Another process holding the mailbox open.
	holder, err := lockShared(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := st.GetMessageProperties(id, mapi.PrMessageClass)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if class, _ := pv.Get(mapi.PrMessageClass); class != "IPM.Note" {
		t.Errorf("converted while the mailbox was busy: class = %v", class)
	}
	if _, err := os.Stat(filepath.Join(dir, smimeUpgradedMarker)); !os.IsNotExist(err) {
		t.Errorf("a busy pass recorded itself as done: %v", err)
	}
	later, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer later.Close()
	pv, err = later.GetMessageProperties(id, mapi.PrMessageClass)
	if err != nil {
		t.Fatal(err)
	}
	if class, _ := pv.Get(mapi.PrMessageClass); class != "IPM.Note.SMIME.MultipartSigned" {
		t.Errorf("the later open did not convert: class = %v", class)
	}
}
