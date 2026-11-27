package oxcmail

import (
	"bytes"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// signedMail is a clear-signed message whose signed part carries a text body and
// a file, as a mail client sends it.
const signedMail = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Signed\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256;\r\n boundary=\"s1\"\r\n\r\n" +
	"This is an S/MIME signed message\r\n--s1\r\n" +
	"Content-Type: multipart/mixed; boundary=\"m1\"\r\n\r\n--m1\r\nContent-Type: text/plain\r\n\r\nsigned  text \r\n" +
	"--m1\r\nContent-Type: application/pdf; name=invoice.pdf\r\nContent-Disposition: attachment; filename=invoice.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nJVBERg==\r\n--m1--\r\n" +
	"--s1\r\nContent-Type: application/pkcs7-signature; name=smime.p7s\r\nContent-Transfer-Encoding: base64\r\n\r\nMIIB\r\n--s1--\r\n"

// TestImportClearSignedAsOneAttachment proves a signed message becomes the object
// Outlook reads a signature from ([MS-OXOSMIME] 2.1.1): the signed class, the body
// its first part promotes, and one attachment holding the multipart/signed entity.
// It used to be stored as plain mail with the signature as a loose file.
func TestImportClearSignedAsOneAttachment(t *testing.T) {
	msg, err := Import([]byte(signedMail), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := propString(msg.Props, mapi.PrMessageClass); got != classSMIMEMultipartSig {
		t.Errorf("class = %q", got)
	}
	if !strings.Contains(propString(msg.Props, mapi.PrBody), "signed  text") {
		t.Errorf("body not promoted: %q", propString(msg.Props, mapi.PrBody))
	}
	if len(msg.Attachments) != 1 || propString(msg.Attachments[0].Props, mapi.PrAttachMimeTag) != "multipart/signed" {
		t.Fatalf("attachments = %+v, want the one signed entity", msg.Attachments)
	}
	// The signed bytes go back out unchanged: the signature still verifies.
	raw, err := Export(msg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	signed := signedMail[strings.Index(signedMail, "This is an S/MIME"):]
	if !bytes.HasSuffix(raw, []byte(signed)) || !bytes.Contains(raw, []byte("Content-Type: multipart/signed;")) {
		t.Errorf("re-export changed the signed entity:\n%s", raw)
	}
}

// TestImportSignedWithoutASignatureKeepsTheEntity imports a multipart/signed a
// sender built with no signature part, against RFC 1847 2.1. The class follows
// the Content-Type, and [MS-OXOSMIME] 3.1.4.1.3 keeps the entity byte for byte,
// so the reader's client finds no signature to verify and reports it; the server
// must neither repair nor invent one on the way out.
func TestImportSignedWithoutASignatureKeepsTheEntity(t *testing.T) {
	const entity = "--s1\r\nContent-Type: text/plain\r\n\r\ntrust me\r\n--s1--\r\n"
	const unsigned = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Fake\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=\"s1\"\r\n\r\n" +
		entity
	msg, err := Import([]byte(unsigned), Options{})
	mustNoErr(t, err, "import a signed message with no signature")
	wantEq(t, propString(msg.Props, mapi.PrMessageClass), classSMIMEMultipartSig, "class")
	raw, err := Export(msg, Options{})
	mustNoErr(t, err, "re-export it")
	if !bytes.HasSuffix(raw, []byte(entity)) {
		t.Errorf("re-export changed the entity:\n%s", raw)
	}
	wantNotContains(t, string(raw), "pkcs7-signature; name", "an invented signature part")
}

// TestImportEncryptedKeepsItsContentType proves an encrypted message takes the
// S/MIME class and keeps its Content-Type, so the re-export still names the
// smime-type a client needs.
func TestImportEncryptedKeepsItsContentType(t *testing.T) {
	const encrypted = "From: alice@hermex.test\r\nTo: bob@hermex.test\r\nSubject: Sealed\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m\r\n" +
		"Content-Disposition: attachment; filename=smime.p7m\r\nContent-Transfer-Encoding: base64\r\n\r\nMIAGCSqG\r\n"
	names := map[mapi.PropertyName]uint16{}
	resolver := func(create bool, ns []mapi.PropertyName) ([]uint16, error) {
		out := make([]uint16, len(ns))
		for i, n := range ns {
			if _, ok := names[n]; !ok && create {
				names[n] = uint16(0x8100 + len(names))
			}
			out[i] = names[n]
		}
		return out, nil
	}
	msg, err := Import([]byte(encrypted), Options{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if got := propString(msg.Props, mapi.PrMessageClass); got != classSMIME || len(msg.Attachments) != 1 {
		t.Fatalf("class = %q attachments = %d", got, len(msg.Attachments))
	}
	raw, err := Export(msg, Options{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("smime-type=enveloped-data")) || !bytes.Contains(raw, []byte("MIAGCSqG")) {
		t.Errorf("re-export lost the S/MIME envelope:\n%s", raw)
	}
}
