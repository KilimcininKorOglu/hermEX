package smime

import "strings"

// mediaType returns the top-level media type of a MIME message, canonicalizing
// line endings first so a message framed with bare LF (as some agents emit) is
// still classified. It returns "" when there is no parseable Content-Type.
func mediaType(raw []byte) string {
	mt, _, err := topMediaType(canonicalizeCRLF(raw))
	if err != nil {
		return ""
	}
	return mt
}

// IsSigned reports whether raw is an RFC 5751 multipart/signed message.
func IsSigned(raw []byte) bool {
	return mediaType(raw) == "multipart/signed"
}

// IsEncrypted reports whether raw is an RFC 5751 application/pkcs7-mime message
// (typically enveloped-data; the legacy application/x-pkcs7-mime spelling is
// included).
func IsEncrypted(raw []byte) bool {
	mt := mediaType(raw)
	return mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime"
}

// IsOpenPGPEncrypted reports whether raw is an OpenPGP/MIME encrypted message
// (RFC 3156 section 4): multipart/encrypted with the application/pgp-encrypted
// protocol.
func IsOpenPGPEncrypted(raw []byte) bool {
	mt, params, err := topMediaType(canonicalizeCRLF(raw))
	return err == nil && mt == "multipart/encrypted" &&
		strings.EqualFold(params["protocol"], "application/pgp-encrypted")
}

// IsSMIME reports whether raw is a signed or encrypted message, S/MIME or
// OpenPGP (a multipart/signed of either protocol is IsSigned). These are exactly
// the messages whose original bytes must be preserved verbatim, because
// re-synthesizing the MIME tree would invalidate the signature or mangle the
// envelope.
func IsSMIME(raw []byte) bool {
	return IsSigned(raw) || IsEncrypted(raw) || IsOpenPGPEncrypted(raw)
}
