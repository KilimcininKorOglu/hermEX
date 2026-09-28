package oxcmail

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// ErrInvalidSMIME reports an S/MIME message object that does not carry the one
// attachment holding its signed or encrypted content ([MS-OXOSMIME] 2.1).
var ErrInvalidSMIME = errors.New("oxcmail: an S/MIME message must carry exactly one attachment")

// CheckSMIME reports ErrInvalidSMIME for an S/MIME message object that is not
// well formed. A submission path calls it before sending, because Export renders
// such an object as a short notice so a stored copy stays readable, and that
// notice must not go out in place of the message.
func CheckSMIME(msg *Message) error {
	if isSMIME, _ := smimeShape(msg); isSMIME && len(msg.Attachments) != 1 {
		return fmt.Errorf("%w: found %d", ErrInvalidSMIME, len(msg.Attachments))
	}
	return nil
}

// smimeShape reports whether the message class names an S/MIME message object
// and, if so, whether it is clear-signed ([MS-OXOSMIME] 3.1.4.1 and 3.1.4.2): a
// class ending in ".SMIME.MultipartSigned" is clear-signed, one ending in ".SMIME"
// is opaque-signed or encrypted. A GpgOL OpenPGP class holds its whole entity the
// way a clear-signed one does, but only when its one attachment is that entity:
// GpgOL gives the class to any message it recognizes as OpenPGP, however it was
// stored, and a message kept as a body with signature or ciphertext attachments
// is ordinary mail.
func smimeShape(msg *Message) (isSMIME, clearSigned bool) {
	class := strings.ToLower(propString(msg.Props, mapi.PrMessageClass))
	switch {
	case strings.HasSuffix(class, ".smime.multipartsigned"):
		return true, true
	case class == strings.ToLower(classGpgOLEncrypted), class == strings.ToLower(classGpgOLSigned):
		return openPGPEntity(msg), true
	case strings.HasSuffix(class, ".smime"):
		return true, false
	}
	return false, false
}

// openPGPEntity reports whether the message's one attachment holds a whole
// OpenPGP/MIME entity: a multipart/signed or an OpenPGP multipart/encrypted.
func openPGPEntity(msg *Message) bool {
	if len(msg.Attachments) != 1 {
		return false
	}
	data, _ := bytesProp(msg.Attachments[0].Props, mapi.PrAttachDataBin)
	root := mime.ParseStructure(data)
	return root.Type == "multipart" && (root.Subtype == "signed" || (root.Subtype == "encrypted" && openPGPProtocol(root)))
}

// writeSMIMEBody writes the content header fields and body of an S/MIME message
// object after its promoted header fields. The attachment's content is the
// message: a clear-signed attachment holds the whole multipart/signed entity,
// Content-Type header included, and is written byte for byte, because any change
// invalidates the signature; an opaque one holds the bare PKCS #7 object, which
// is written base64-encoded under the Content-Type the client stored
// ([MS-OXOSMIME] 3.1.4.1.3 and 3.1.4.2.3). An object without that one attachment
// is rendered as a notice saying so, the way a stored copy stays readable.
func writeSMIMEBody(b *bytes.Buffer, msg *Message, opt Options, clearSigned bool) {
	if len(msg.Attachments) != 1 {
		writeField(b, "Content-Type", "text/plain; charset=utf-8")
		b.WriteString("\r\n")
		fmt.Fprintf(b, "[This message is not a valid S/MIME message: it holds %d attachments, but exactly one is required.]\r\n", len(msg.Attachments))
		return
	}
	att := msg.Attachments[0]
	data, _ := bytesProp(att.Props, mapi.PrAttachDataBin)
	if clearSigned {
		b.Write(data)
		return
	}
	filename := headerParam(propString(att.Props, mapi.PrAttachLongFilename))
	if filename == "" {
		filename = "smime.p7m"
	}
	writeField(b, "Content-Type", opaqueContentType(msg, opt, att, filename))
	writeField(b, "Content-Disposition", "attachment"+filenameParam(filename))
	writeField(b, "Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	b.Write(encodeBase64(data))
}

// opaqueContentType is the Content-Type of an opaque S/MIME message: the
// "Content-Type" internet header the client stored on the message, which carries
// the smime-type parameter, else the attachment's MIME type named after its file.
func opaqueContentType(msg *Message, opt Options, att Attachment, filename string) string {
	if stored := storedContentType(msg.Props, opt); stored != "" {
		return stored
	}
	mimeType := headerLineBreaks.Replace(propString(att.Props, mapi.PrAttachMimeTag))
	if mimeType == "" {
		mimeType = "application/pkcs7-mime"
	}
	return mimeType + nameParam(filename)
}

// storedContentType reads the PS_INTERNET_HEADERS "Content-Type" named property,
// or "" when the message carries none or no resolver is given.
func storedContentType(props mapi.PropertyValues, opt Options) string {
	if opt.Resolver == nil {
		return ""
	}
	ids, err := opt.Resolver(false, []mapi.PropertyName{mapi.NameInternetContentType})
	if err != nil || len(ids) != 1 || ids[0] == 0 {
		return ""
	}
	v, ok := props.GetAnyCharset(mapi.MakeTag(ids[0], mapi.PtUnicode))
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return headerLineBreaks.Replace(strings.TrimSpace(s))
}
