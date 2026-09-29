package oxcmail

import (
	"bytes"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// pgpMail builds an OpenPGP/MIME message (RFC 3156) of the given type and
// protocol around payload.
func pgpMail(mediaType, protocol, payload string) string {
	return "From: sender@example.org\r\nTo: recipient@example.org\r\nSubject: OpenPGP transport\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: " + mediaType + "; protocol=\"" + protocol + "\"; boundary=\"pgp-boundary\"\r\n\r\n" + payload
}

const (
	pgpEncryptedPayload = "--pgp-boundary\r\nContent-Type: application/pgp-encrypted\r\n\r\nVersion: 1\r\n" +
		"--pgp-boundary\r\nContent-Type: application/octet-stream\r\n\r\n" +
		"-----BEGIN PGP MESSAGE-----\r\n\r\nopaque-ciphertext\r\n-----END PGP MESSAGE-----\r\n--pgp-boundary--\r\n"
	pgpSignedPayload = "--pgp-boundary\r\nContent-Type: text/plain;\r\n\tcharset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\nFirst line=20\r\nSecond =C3=A4 line\r\n\r\n" +
		"--pgp-boundary\r\nContent-Type: application/pgp-signature\r\n\r\n" +
		"-----BEGIN PGP SIGNATURE-----\r\n\r\nopaque-signature\r\n-----END PGP SIGNATURE-----\r\n--pgp-boundary--\r\n"
)

// namedResolver is an in-memory named-property allocator.
func namedResolver() (func(bool, []mapi.PropertyName) ([]uint16, error), map[mapi.PropertyName]uint16) {
	names := map[mapi.PropertyName]uint16{}
	return func(create bool, ns []mapi.PropertyName) ([]uint16, error) {
		out := make([]uint16, len(ns))
		for i, n := range ns {
			if _, ok := names[n]; !ok && create {
				names[n] = uint16(0x8100 + len(names))
			}
			out[i] = names[n]
		}
		return out, nil
	}, names
}

// TestOpenPGPRoundTrip proves an OpenPGP signed or encrypted message becomes the
// object GpgOL reads (its message class, the GpgOL class override and one
// attachment holding the whole entity), and that export writes the entity back
// byte for byte, so the signature still verifies and the ciphertext still opens.
func TestOpenPGPRoundTrip(t *testing.T) {
	for _, c := range []struct {
		mediaType, protocol, payload, class, gpgolClass, mimeTag string
	}{
		{"multipart/encrypted", "application/pgp-encrypted", pgpEncryptedPayload,
			classGpgOLEncrypted, classGpgOLEncrypted, "multipart/encrypted"},
		{"multipart/signed", "application/pgp-signature", pgpSignedPayload,
			classSMIMEMultipartSig, classGpgOLSigned, "multipart/signed"},
	} {
		resolver, names := namedResolver()
		msg, err := Import([]byte(pgpMail(c.mediaType, c.protocol, c.payload)), Options{Resolver: resolver})
		mustImport(t, err)
		wantProp(t, c.mediaType+" class", propString(msg.Props, mapi.PrMessageClass), c.class)
		id := names[mapi.NameGpgOLMsgClass]
		wantProp(t, c.mediaType+" GpgOL class", propString(msg.Props, mapi.MakeTag(id, mapi.PtString8)), c.gpgolClass)
		if len(msg.Attachments) != 1 {
			t.Fatalf("%s: %d attachments, want the one entity", c.mediaType, len(msg.Attachments))
		}
		wantProp(t, c.mediaType+" attachment type", propString(msg.Attachments[0].Props, mapi.PrAttachMimeTag), c.mimeTag)

		// The class Outlook stores after GpgOL handles the message is an InfoPath
		// variant of the clear-signed one; it must export the entity the same way.
		for _, class := range []string{c.class, "IPM.Note.InfoPathForm.GpgOL.SMIME.MultipartSigned", "IPM.Note.InfoPathForm.GpgOLS.SMIME.MultipartSigned"} {
			msg.Props.Set(mapi.PrMessageClass, class)
			raw, err := Export(msg, Options{Resolver: resolver})
			mustImport(t, err)
			if !bytes.HasSuffix(raw, []byte(c.payload)) || !bytes.Contains(raw, []byte("Content-Type: "+c.mediaType+"; protocol=\""+c.protocol+"\"")) {
				t.Errorf("%s as %s: re-export changed the entity:\n%s", c.mediaType, class, raw)
			}
		}
	}
}

// TestOpenPGPKeepsTheBlindRecipient proves an OpenPGP message keeps its Bcc
// recipient in the recipient table, where the sender's Sent Items copy reads it.
func TestOpenPGPKeepsTheBlindRecipient(t *testing.T) {
	mail := strings.Replace(pgpMail("multipart/signed", "application/pgp-signature", pgpSignedPayload),
		"To: recipient@example.org\r\n", "To: recipient@example.org\r\nBcc: hidden@example.org\r\n", 1)
	msg, err := Import([]byte(mail), Options{})
	mustImport(t, err)
	found := false
	for _, r := range msg.Recipients {
		if rt, _ := r.Get(mapi.PrRecipientType); rt == int32(mapi.RecipBcc) {
			found = true
		}
	}
	if !found {
		t.Errorf("recipients %v lack the Bcc recipient", msg.Recipients)
	}
}

// TestEncryptedOfAnotherProtocolIsNotOpenPGP proves a multipart/encrypted message of another
// protocol is not taken for OpenPGP.
func TestEncryptedOfAnotherProtocolIsNotOpenPGP(t *testing.T) {
	msg, err := Import([]byte(pgpMail("multipart/encrypted", "application/x-other", pgpEncryptedPayload)), Options{})
	mustImport(t, err)
	if got := propString(msg.Props, mapi.PrMessageClass); strings.Contains(got, "GpgOL") {
		t.Errorf("class = %q, want plain mail", got)
	}
}

// TestGpgOLClassWithoutTheEntityIsRegularMail proves a message GpgOL gave its
// class to but that holds a body and loose signature or ciphertext attachments is
// exported as the mail it is, not as the invalid-message notice, and may be sent.
func TestGpgOLClassWithoutTheEntityIsRegularMail(t *testing.T) {
	for _, class := range []string{classGpgOLSigned, classGpgOLEncrypted} {
		msg := &Message{}
		msg.Props.Set(mapi.PrMessageClass, class)
		msg.Props.Set(mapi.PrBody, "the readable body")
		for _, name := range []string{"signature.asc", "msg.asc"} {
			var a Attachment
			a.Props.Set(mapi.PrAttachLongFilename, name)
			a.Props.Set(mapi.PrAttachDataBin, []byte("-----BEGIN PGP-----"))
			msg.Attachments = append(msg.Attachments, a)
		}
		if err := CheckSMIME(msg); err != nil {
			t.Errorf("%s: CheckSMIME = %v, want nil", class, err)
		}
		raw, err := Export(msg, Options{})
		mustImport(t, err)
		if !bytes.Contains(raw, []byte("the readable body")) || bytes.Contains(raw, []byte("not a valid S/MIME")) {
			t.Errorf("%s: exported as the notice:\n%s", class, raw)
		}
	}
}

func mustImport(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantProp(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}
