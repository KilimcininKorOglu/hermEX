package smime

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/smallstep/pkcs7"
)

// Sign wraps content — a complete inner MIME entity (its own Content-Type header,
// a blank line, then the body, with CRLF line endings) — in an RFC 5751
// multipart/signed entity carrying a detached SHA-256 PKCS#7 signature
// (smime.p7s). The returned bytes start with the multipart/signed Content-Type
// header, so the caller splices the message identity headers (From/To/Subject/
// Date/Message-ID) above them. content is emitted verbatim between the
// boundaries and is exactly what the signature covers, so a verifier recomputes
// the same digest.
func Sign(content []byte, cert *x509.Certificate, key crypto.PrivateKey) ([]byte, error) {
	sd, err := pkcs7.NewSignedData(content)
	if err != nil {
		return nil, fmt.Errorf("smime: new signed data: %w", err)
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSigner(cert, key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("smime: add signer: %w", err)
	}
	sd.Detach() // a multipart/signed signature is detached from the content
	der, err := sd.Finish()
	if err != nil {
		return nil, fmt.Errorf("smime: finish signature: %w", err)
	}
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=\"sha-256\"; boundary=\"%s\"\r\n", boundary)
	b.WriteString("MIME-Version: 1.0\r\n\r\n")
	b.WriteString("This is an S/MIME signed message\r\n\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.Write(content)
	fmt.Fprintf(&b, "\r\n--%s\r\n", boundary)
	b.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n\r\n")
	b.WriteString(base64Wrap(der))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return b.Bytes(), nil
}

// Verify checks the signature of an RFC 5751 multipart/signed message and returns
// the signer's leaf certificate and the signed content (the inner MIME entity).
// A valid signature returns a nil error even when the signer's certificate is
// self-signed or issued by an unknown CA: the caller decides how far to trust the
// returned certificate (for example by matching it to the From address).
// Certificate-chain and trust validation is intentionally left to the caller.
func Verify(raw []byte) (signer *x509.Certificate, content []byte, err error) {
	raw = canonicalizeCRLF(raw) // inbound framing may use bare LF; the signature is over CRLF
	mt, params, err := topMediaType(raw)
	if err != nil {
		return nil, nil, err
	}
	if mt != "multipart/signed" {
		return nil, nil, ErrNotSigned
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, nil, errors.New("smime: multipart/signed without a boundary")
	}
	content, sigDER, err := splitSigned(raw, boundary)
	if err != nil {
		return nil, nil, err
	}
	p7, err := pkcs7.Parse(sigDER)
	if err != nil {
		return nil, nil, fmt.Errorf("smime: parse signature: %w", err)
	}
	p7.Content = content // detached: verify the signature against the wire content
	if err := p7.Verify(); err != nil {
		return nil, nil, fmt.Errorf("smime: signature verification failed: %w", err)
	}
	return p7.GetOnlySigner(), content, nil
}

// splitSigned extracts the signed content (the first multipart part, verbatim)
// and the detached PKCS#7 signature DER (the second part, base64-decoded) from a
// multipart/signed message. The CRLF immediately before a boundary is part of the
// delimiter, not the content, so it is excluded from the signed bytes.
func splitSigned(raw []byte, boundary string) (content, sigDER []byte, err error) {
	delim := []byte("--" + boundary)
	open := bytes.Index(raw, delim)
	if open < 0 {
		return nil, nil, errors.New("smime: opening boundary not found")
	}
	nl := bytes.Index(raw[open:], []byte("\r\n"))
	if nl < 0 {
		return nil, nil, errors.New("smime: malformed first part")
	}
	partStart := open + nl + 2

	// Content runs to the CRLF that precedes the next boundary.
	next := bytes.Index(raw[partStart:], append([]byte("\r\n"), delim...))
	if next < 0 {
		return nil, nil, errors.New("smime: signature boundary not found")
	}
	content = raw[partStart : partStart+next]

	// The signature part follows: skip its delimiter line and its own headers,
	// then base64-decode its body up to the closing boundary.
	sigPart := raw[partStart+next+2:] // past the "\r\n" before the delimiter
	_, sigPart, ok := bytes.Cut(sigPart, []byte("\r\n"))
	if !ok {
		return nil, nil, errors.New("smime: malformed signature part")
	}
	_, body, ok := bytes.Cut(sigPart, []byte("\r\n\r\n"))
	if !ok {
		return nil, nil, errors.New("smime: signature part has no body")
	}
	if e := bytes.Index(body, delim); e >= 0 {
		body = body[:e]
	}
	sigDER, err = decodeBase64Body(body)
	if err != nil {
		return nil, nil, fmt.Errorf("smime: decode signature: %w", err)
	}
	return content, sigDER, nil
}
