package activesync

import "hermex/internal/smime"

// EAS message classes for an email item. The store keeps the generic
// PidTagMessageClass "IPM.Note" for every delivered message (the MIME->MAPI
// import does not classify S/MIME), so the S/MIME class is recovered at render
// time from the message's media type: a clear-signed message is
// IPM.Note.SMIME.MultipartSigned, an encrypted or opaque-signed one is
// IPM.Note.SMIME (MS-OXOSMIME 2.1.1). Surfacing the distinct class lets the
// device hand the message to its S/MIME handler instead of rendering the
// signature or ciphertext as if it were the body. This is presentation-layer
// classification, kept local to the protocol that needs it.
const (
	messageClassNote           = "IPM.Note"
	messageClassSMIME          = "IPM.Note.SMIME"
	messageClassSMIMEMultipart = "IPM.Note.SMIME.MultipartSigned"
)

// MIMESupport (MS-ASCMD 2.2.3.100) is the client's declared appetite for a MIME
// body: never send one (0), send one only for S/MIME messages (1), or send one
// for every message (2). A signed or encrypted message is served as verbatim
// MIME only when the client advertised at least mimeSupportSMIME; a client that
// did not still learns the class and receives the best-effort extracted body.
const (
	mimeSupportNever = 0
	mimeSupportSMIME = 1
	mimeSupportAll   = 2
)

// messageClassFor derives the EAS message class from the raw MIME. A
// multipart/signed body is clear-signed; an application/pkcs7-mime body is
// encrypted or opaque-signed; anything else is a plain note. smime.IsSigned keys
// on the top-level media type, so a PGP/MIME multipart/signed body is also
// labeled MultipartSigned: forcing full MIME is still the correct handling, the
// device's signature handler simply finds a PGP signature rather than a PKCS#7
// one.
func messageClassFor(raw []byte) string {
	switch {
	case smime.IsSigned(raw):
		return messageClassSMIMEMultipart
	case smime.IsEncrypted(raw):
		return messageClassSMIME
	default:
		return messageClassNote
	}
}
