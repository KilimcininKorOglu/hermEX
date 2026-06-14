// Package smime implements server-side S/MIME (RFC 5751): signing, verifying,
// encrypting, and decrypting mail. It wraps the CMS/PKCS#7 layer — SignedData for
// signatures, EnvelopedData for encryption — and frames the results as the
// multipart/signed and application/pkcs7-mime MIME entities that S/MIME clients
// (Outlook, Thunderbird) interoperate with. Content is treated as opaque bytes
// with CRLF line endings, as S/MIME requires, and never re-encoded, so a
// signature stays valid.
package smime

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"strings"
)

// ErrNotSigned and ErrNotEncrypted report that a message is not the S/MIME type
// a call expects, so a caller can distinguish "plain message" from a real error.
var (
	ErrNotSigned    = errors.New("smime: not a multipart/signed message")
	ErrNotEncrypted = errors.New("smime: not an application/pkcs7-mime message")
)

// randomBoundary returns a MIME multipart boundary unlikely to collide with the
// content it wraps. The base64 content alphabet excludes '-', so a "--boundary"
// delimiter can never appear inside an encoded part.
func randomBoundary() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("hermex-smime-%x", b), nil
}

// base64Wrap base64-encodes der and folds it into 64-character CRLF lines, the
// transfer encoding S/MIME parts use.
func base64Wrap(der []byte) string {
	enc := base64.StdEncoding.EncodeToString(der)
	var b strings.Builder
	for len(enc) > 64 {
		b.WriteString(enc[:64])
		b.WriteString("\r\n")
		enc = enc[64:]
	}
	b.WriteString(enc)
	return b.String()
}

// topMediaType parses the leading Content-Type of a MIME entity (the header block
// before the first blank line) into its media type and parameters.
func topMediaType(raw []byte) (mediatype string, params map[string]string, err error) {
	headerBlock := raw
	if before, _, ok := bytes.Cut(raw, []byte("\r\n\r\n")); ok {
		headerBlock = before
	}
	ct := headerValue(headerBlock, "Content-Type")
	if ct == "" {
		return "", nil, errors.New("smime: message has no Content-Type header")
	}
	return mime.ParseMediaType(ct)
}

// headerValue returns the unfolded value of the named header from a CRLF header
// block, matched case-insensitively, or "" when absent.
func headerValue(headerBlock []byte, name string) string {
	lines := strings.Split(string(headerBlock), "\r\n")
	want := strings.ToLower(name)
	for i := 0; i < len(lines); i++ {
		colon := strings.IndexByte(lines[i], ':')
		if colon < 0 || strings.ToLower(strings.TrimSpace(lines[i][:colon])) != want {
			continue
		}
		var val strings.Builder
		val.WriteString(lines[i][colon+1:])
		for i+1 < len(lines) && len(lines[i+1]) > 0 && (lines[i+1][0] == ' ' || lines[i+1][0] == '\t') {
			i++
			val.WriteString(lines[i])
		}
		return strings.TrimSpace(val.String())
	}
	return ""
}

// canonicalizeCRLF normalizes line endings to CRLF. S/MIME signatures are
// computed over the CRLF-canonical form, but agents (openssl among them) write
// the surrounding MIME framing with bare LF, so an inbound message must be
// canonicalized before its boundaries are located and its signed content is
// recovered. A message that is already all-CRLF is returned unchanged.
func canonicalizeCRLF(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

// decodeBase64Body strips transfer-encoding whitespace from a base64 part body
// and decodes it.
func decodeBase64Body(body []byte) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, string(body))
	return base64.StdEncoding.DecodeString(clean)
}
