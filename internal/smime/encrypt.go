package smime

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/smallstep/pkcs7"
)

// init selects AES-256-CBC as the EnvelopedData content-encryption algorithm,
// the S/MIME default cipher (RFC 5751). The pkcs7 setting is a package global and
// hermex always encrypts with AES-256-CBC, so it is set once at load.
func init() {
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
}

// Encrypt enciphers inner, a complete MIME entity, to the recipient
// certificates as an RFC 5751 application/pkcs7-mime enveloped-data entity
// (smime.p7m): AES-256-CBC content encryption under a fresh key, that key
// RSA-wrapped to each recipient. The returned bytes start with the
// application/pkcs7-mime Content-Type header, so the caller splices the message
// identity headers (From/To/Subject/...) above them. Only a holder of a
// recipient private key can recover inner.
func Encrypt(inner []byte, recipients []*x509.Certificate) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("smime: no recipient certificates")
	}
	der, err := pkcs7.Encrypt(inner, recipients)
	if err != nil {
		return nil, fmt.Errorf("smime: encrypt: %w", err)
	}
	return []byte("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"smime.p7m\"\r\n" +
		"MIME-Version: 1.0\r\n\r\n" +
		base64Wrap(der) + "\r\n"), nil
}

// Decrypt recovers the inner MIME entity from an RFC 5751 application/pkcs7-mime
// enveloped-data message using a recipient's certificate and private key. The
// legacy application/x-pkcs7-mime media type is also accepted.
func Decrypt(raw []byte, cert *x509.Certificate, key crypto.PrivateKey) ([]byte, error) {
	raw = canonicalizeCRLF(raw)
	mt, _, err := topMediaType(raw)
	if err != nil {
		return nil, err
	}
	if mt != "application/pkcs7-mime" && mt != "application/x-pkcs7-mime" {
		return nil, ErrNotEncrypted
	}
	_, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		return nil, errors.New("smime: enveloped message has no body")
	}
	der, err := decodeBase64Body(body)
	if err != nil {
		return nil, fmt.Errorf("smime: decode enveloped data: %w", err)
	}
	p7, err := pkcs7.Parse(der)
	if err != nil {
		return nil, fmt.Errorf("smime: parse enveloped data: %w", err)
	}
	content, err := decryptEnveloped(p7, cert, key)
	if err != nil {
		return nil, fmt.Errorf("smime: decrypt: %w", err)
	}
	return content, nil
}

// errBadPadding reports enveloped content whose CBC padding does not fit it.
var errBadPadding = errors.New("smime: enveloped content padding is invalid")

// decryptEnveloped runs the library decryption. The library's unpad slices the
// content by the padding length the last plaintext byte states without checking
// it against the content length, so a sender who alters the last ciphertext block
// makes it panic. That panic is returned as errBadPadding; it is the only one
// this call is known to raise, and the message is unreadable either way.
func decryptEnveloped(p7 *pkcs7.PKCS7, cert *x509.Certificate, key crypto.PrivateKey) (content []byte, err error) {
	defer func() {
		if p := recover(); p != nil {
			content, err = nil, fmt.Errorf("%w: %v", errBadPadding, p)
		}
	}()
	return p7.Decrypt(cert, key)
}
