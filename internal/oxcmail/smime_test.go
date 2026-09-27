package oxcmail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// signedEntity is the outer multipart/signed entity Outlook stores as the one
// attachment of a clear-signed message, Content-Type header included.
const signedEntity = "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=\"b1\"\r\n" +
	"\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsigned  text \r\n" +
	"--b1\r\nContent-Type: application/pkcs7-signature; name=smime.p7s\r\nContent-Transfer-Encoding: base64\r\n\r\nMIIB\r\n--b1--\r\n"

func smimeMessage(class string, atts ...Attachment) *Message {
	return &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: class},
		{Tag: mapi.PrSubject, Value: "Signed"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
	}, Attachments: atts}
}

func smimeAttachment(mimeTag string, data []byte) Attachment {
	return Attachment{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(1)},
		{Tag: mapi.PrAttachMimeTag, Value: mimeTag},
		{Tag: mapi.PrAttachDataBin, Value: data},
	}}
}

// TestExportClearSignedKeepsTheSignedEntity proves a clear-signed message goes
// out as its stored multipart/signed entity, byte for byte, under the promoted
// header fields. It used to be wrapped as an ordinary attachment in a
// multipart/mixed, so no recipient could verify the signature.
func TestExportClearSignedKeepsTheSignedEntity(t *testing.T) {
	raw, err := Export(smimeMessage("IPM.Note.SMIME.MultipartSigned",
		smimeAttachment("multipart/signed", []byte(signedEntity))), Options{})
	if err != nil {
		t.Fatal(err)
	}
	head, body, ok := bytes.Cut(raw, []byte("\r\nContent-Type: "))
	if !ok || !strings.Contains(string(head), "Subject: Signed") {
		t.Fatalf("no promoted header block before the entity:\n%s", raw)
	}
	if got := "Content-Type: " + string(body); got != signedEntity {
		t.Errorf("entity changed:\n%q\nwant\n%q", got, signedEntity)
	}
}

// TestExportOpaqueCarriesThePKCS7Object proves an opaque message goes out as its
// PKCS #7 object, base64-encoded, under the Content-Type the client stored.
func TestExportOpaqueCarriesThePKCS7Object(t *testing.T) {
	der := []byte{0x30, 0x82, 0x01, 0x00, 0xde, 0xad}
	msg := smimeMessage("IPM.Note.SMIME", smimeAttachment("application/pkcs7-mime", der))
	const stored = "application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m"
	msg.Props.Set(mapi.MakeTag(0x8100, mapi.PtUnicode), stored)
	resolver := func(_ bool, names []mapi.PropertyName) ([]uint16, error) {
		if len(names) == 1 && names[0] == mapi.NameInternetContentType {
			return []uint16{0x8100}, nil
		}
		return make([]uint16, len(names)), nil
	}
	raw, err := Export(msg, Options{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"Content-Type: " + stored, "Content-Transfer-Encoding: base64", base64.StdEncoding.EncodeToString(der)} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "multipart/mixed") {
		t.Errorf("the object was wrapped as an attachment:\n%s", s)
	}
}

// TestMalformedSMIMEIsRefusedForSendingAndReadableStored keeps a malformed S/MIME
// object from going out, while its stored copy still renders as a notice.
func TestMalformedSMIMEIsRefusedForSendingAndReadableStored(t *testing.T) {
	msg := smimeMessage("IPM.Note.SMIME")
	if err := CheckSMIME(msg); !errors.Is(err, ErrInvalidSMIME) {
		t.Errorf("CheckSMIME = %v, want ErrInvalidSMIME", err)
	}
	raw, err := Export(msg, Options{})
	if err != nil || !strings.Contains(string(raw), "not a valid S/MIME message") {
		t.Errorf("export = %v\n%s", err, raw)
	}
	if err := CheckSMIME(smimeMessage("IPM.Note")); err != nil {
		t.Errorf("a plain message was refused: %v", err)
	}
}
