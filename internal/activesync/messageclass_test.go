package activesync

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// signedFixture is a clear-signed (multipart/signed) message: a text/plain body
// part followed by a detached PKCS#7 signature carried as an attachment part.
const signedFixture = "From: a@hermex.test\r\n" +
	"To: b@hermex.test\r\n" +
	"Subject: Signed hello\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256; boundary=\"sig\"\r\n" +
	"\r\n" +
	"--sig\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"This is the signed body.\r\n" +
	"--sig\r\n" +
	"Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n" +
	"Content-Disposition: attachment; filename=\"smime.p7s\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"MIIBODCCAd4CAQEwCzAJBgUrDgMCGgUA\r\n" +
	"--sig--\r\n"

// encryptedFixture is an opaque (application/pkcs7-mime) enveloped-data message:
// the whole body is ciphertext, with no extractable plaintext part.
const encryptedFixture = "From: a@hermex.test\r\n" +
	"To: b@hermex.test\r\n" +
	"Subject: Encrypted hello\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"smime.p7m\"\r\n" +
	"\r\n" +
	"MIIBd0YJKoZIhvcNAQcDoIIBaDCCAWQCAQAxgc0wgcoCAQAwczBmMQswCQ==\r\n"

// TestMessageClassFor proves the S/MIME class is recovered from the media type:
// a multipart/signed body is clear-signed, an application/pkcs7-mime body is
// encrypted/opaque-signed, and a plain message keeps IPM.Note.
func TestMessageClassFor(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"clear-signed is MultipartSigned", signedFixture, messageClassSMIMEMultipart},
		{"pkcs7-mime is SMIME", encryptedFixture, messageClassSMIME},
		{"a plain note keeps IPM.Note", convMsgRoot, messageClassNote},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := messageClassFor([]byte(c.raw)); got != c.want {
				t.Errorf("messageClassFor = %q, want %q", got, c.want)
			}
		})
	}
}

// TestParseBodyPrefMIMESupport proves Options>MIMESupport is read alongside the
// BodyPreference and preserved on the bodyPref.
func TestParseBodyPrefMIMESupport(t *testing.T) {
	c := wbxml.Elem(wbxml.ASCollection,
		wbxml.Elem(wbxml.ASOptions,
			wbxml.Elem(wbxml.ABBodyPreference, wbxml.Str(wbxml.ABType, strconv.Itoa(bodyTypePlain))),
			wbxml.Str(wbxml.ASMIMESupport, strconv.Itoa(mimeSupportSMIME))))
	pref := parseBodyPref(c)
	if pref.typ != bodyTypePlain {
		t.Errorf("typ = %d, want %d", pref.typ, bodyTypePlain)
	}
	if pref.mimeSupport != mimeSupportSMIME {
		t.Errorf("mimeSupport = %d, want %d", pref.mimeSupport, mimeSupportSMIME)
	}
}

// TestEmailAppDataSMIMEForcesMIME is the discriminating case: a clear-signed
// message, a client asking for plain text but advertising MIMESupport=SMIME. The
// body must be served as verbatim full MIME (so the signature survives), the item
// must carry the IPM.Note.SMIME.MultipartSigned class, and the signature part must
// not be surfaced as a bogus AirSyncBase attachment.
func TestEmailAppDataSMIMEForcesMIME(t *testing.T) {
	raw := []byte(signedFixture)
	m := objectstore.MessageInfo{UID: 1, Subject: "Signed hello", Sender: "a@hermex.test",
		InternalDate: time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)}
	pref := bodyPref{typ: bodyTypePlain, mimeSupport: mimeSupportSMIME}
	data := emailAppData(raw, m, "1", "5", pref)

	if cls := data.ChildText(wbxml.EMMessageClass); cls != messageClassSMIMEMultipart {
		t.Errorf("message class = %q, want %q", cls, messageClassSMIMEMultipart)
	}
	body := data.Child(wbxml.ABBody)
	if body == nil {
		t.Fatal("no Body element")
	}
	if bt := body.ChildText(wbxml.ABType); bt != strconv.Itoa(bodyTypeMIME) {
		t.Errorf("body type = %q, want %d (full MIME preserves the signature)", bt, bodyTypeMIME)
	}
	d := body.Child(wbxml.ABData)
	if d == nil || !bytes.Equal(d.Opaque, raw) {
		t.Error("forced MIME body must carry the verbatim S/MIME bytes")
	}
	if data.Child(wbxml.ABAttachments) != nil {
		t.Error("the signature part must not be surfaced as a bogus attachment on a forced-MIME body")
	}
}

// TestEmailAppDataSMIMEClientNoMIME proves the gate: the same clear-signed message
// to a client that advertised MIMESupport=0 is NOT forced to MIME. The client still
// learns the S/MIME class, but gets the best-effort extracted body it can render.
func TestEmailAppDataSMIMEClientNoMIME(t *testing.T) {
	raw := []byte(signedFixture)
	m := objectstore.MessageInfo{UID: 1, Subject: "Signed hello", Sender: "a@hermex.test",
		InternalDate: time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)}
	pref := bodyPref{typ: bodyTypePlain, mimeSupport: mimeSupportNever}
	data := emailAppData(raw, m, "1", "5", pref)

	if cls := data.ChildText(wbxml.EMMessageClass); cls != messageClassSMIMEMultipart {
		t.Errorf("message class = %q, want %q (the class is reported even without MIME support)", cls, messageClassSMIMEMultipart)
	}
	if bt := data.Child(wbxml.ABBody).ChildText(wbxml.ABType); bt != strconv.Itoa(bodyTypePlain) {
		t.Errorf("body type = %q, want %d (a no-MIME client gets the extracted body)", bt, bodyTypePlain)
	}
}

// TestEmailAppDataPlainUnaffected proves a non-S/MIME message is untouched by the
// new path: MIMESupport=SMIME must not force MIME on a plain note (forceMIME only
// fires for an S/MIME class), so the plain body is still extracted as Type 1.
func TestEmailAppDataPlainUnaffected(t *testing.T) {
	m := objectstore.MessageInfo{UID: 1, Subject: "Project plan", Sender: "a@hermex.test",
		InternalDate: time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)}
	pref := bodyPref{typ: bodyTypePlain, mimeSupport: mimeSupportSMIME}
	data := emailAppData([]byte(convMsgRoot), m, "1", "5", pref)

	if cls := data.ChildText(wbxml.EMMessageClass); cls != messageClassNote {
		t.Errorf("plain message class = %q, want %q", cls, messageClassNote)
	}
	if bt := data.Child(wbxml.ABBody).ChildText(wbxml.ABType); bt != strconv.Itoa(bodyTypePlain) {
		t.Errorf("plain body type = %q, want %d (MIMESupport must not force MIME on a non-S/MIME message)", bt, bodyTypePlain)
	}
}
