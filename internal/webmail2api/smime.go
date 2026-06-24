package webmail2api

import (
	"bytes"
	"crypto"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/smime"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// smimeStatus describes a received message's S/MIME state for the reader banner.
type smimeStatus struct {
	Signed    bool
	Encrypted bool
	Verified  bool
	SignedBy  string
}

// certEmail returns a certificate's email address (its SAN, or its common name
// when that looks like an address), used for the signer label and for harvesting.
func certEmail(cert *x509.Certificate) string {
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	return cert.Subject.CommonName
}

// smimeOpen inspects a received message: it decrypts with the caller's key when
// encrypted, verifies the signature when signed, and returns the inner content
// plus the status. A non-S/MIME message passes through unchanged.
//
// Verified means only that the signature matches the certificate embedded in the
// message — it is NOT a chain/identity check, so the cert is not trusted or
// stored for later encryption (that would be trust-on-first-use, letting a
// self-signed cert claiming an address poison the encryption path).
func (s *Server) smimeOpen(st *objectstore.Store, raw []byte) ([]byte, smimeStatus) {
	var status smimeStatus
	content := raw
	if smime.IsEncrypted(content) {
		status.Encrypted = true
		if key, cert, ok := unlockSmimeIdentity(st, s.secret); ok {
			if dec, err := smime.Decrypt(content, cert, key); err == nil {
				content = dec
			}
		}
	}
	if smime.IsSigned(content) {
		status.Signed = true
		if signer, _, err := smime.Verify(content); err == nil {
			status.Verified = true
			status.SignedBy = certEmail(signer)
		}
	}
	return content, status
}

// applySmime signs and/or encrypts a built message before sending. The outer
// identity headers (From/To/Subject) are split off and re-attached around the
// S/MIME entity, so the delivered message still carries them. Signing uses the
// caller's stored identity; encrypting requires a stored certificate for every
// recipient. Sign-then-encrypt is applied (the standard order).
func (s *Server) applySmime(mailbox string, raw []byte, recipients []string, sign, encrypt bool) ([]byte, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, errors.New("mailbox unavailable")
	}
	defer st.Close()
	identity, inner := splitForSmime(raw)
	if sign {
		key, cert, ok := unlockSmimeIdentity(st, s.secret)
		if !ok {
			return nil, errors.New("no S/MIME identity is set up; upload one in Settings")
		}
		signed, err := smime.Sign(inner, cert, key)
		if err != nil {
			return nil, fmt.Errorf("could not sign the message: %w", err)
		}
		if !encrypt {
			return append(identity, signed...), nil
		}
		inner = signed // sign-then-encrypt: the signed entity becomes the enveloped content
	}
	certs := make([]*x509.Certificate, 0, len(recipients)+1)
	for _, addr := range recipients {
		der, ok, _ := st.GetRecipientCert(strings.ToLower(strings.TrimSpace(addr)))
		if !ok {
			return nil, fmt.Errorf("no S/MIME certificate stored for %s, so the message cannot be encrypted", addr)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("the stored certificate for %s is invalid", addr)
		}
		certs = append(certs, cert)
	}
	// Encrypt to the sender's own certificate too, so the Sent copy stays readable.
	if _, ownCert, ok := unlockSmimeIdentity(st, s.secret); ok {
		certs = append(certs, ownCert)
	}
	env, err := smime.Encrypt(inner, certs)
	if err != nil {
		return nil, fmt.Errorf("could not encrypt the message: %w", err)
	}
	return append(identity, env...), nil
}

// splitForSmime divides an RFC 5322 message into its identity headers (From, To,
// Subject, Date — everything that stays on the outer message) and the inner MIME
// entity (the Content-* headers and the body) that S/MIME signs or encrypts.
// MIME-Version is dropped from both: the S/MIME wrapper emits its own.
func splitForSmime(raw []byte) (identityHeaders, innerEntity []byte) {
	hdr, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		return raw, nil
	}
	var ident, content bytes.Buffer
	for _, line := range splitHeaderLines(hdr) {
		name := strings.ToLower(headerName(line))
		switch {
		case name == "mime-version":
		case strings.HasPrefix(name, "content-"):
			content.Write(line)
			content.WriteString("\r\n")
		default:
			ident.Write(line)
			ident.WriteString("\r\n")
		}
	}
	content.WriteString("\r\n")
	content.Write(body)
	return ident.Bytes(), content.Bytes()
}

// splitHeaderLines splits a CRLF header block into logical headers, keeping each
// folded continuation joined to its header line.
func splitHeaderLines(hdr []byte) [][]byte {
	lines := strings.Split(string(hdr), "\r\n")
	var out [][]byte
	for _, l := range lines {
		if l != "" && (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], append([]byte("\r\n"), l...)...)
			continue
		}
		out = append(out, []byte(l))
	}
	return out
}

// headerName returns the field name of a header line (the text before the colon).
func headerName(line []byte) string {
	name, _, found := bytes.Cut(line, []byte{':'})
	if !found {
		return ""
	}
	return string(bytes.TrimSpace(name))
}

// certInfo projects an x509 certificate into the SPA's SMIMECertInfo shape.
func certInfo(cert *x509.Certificate, hasKey bool) map[string]any {
	fp := sha256.Sum256(cert.Raw)
	return map[string]any{
		"subject":       cert.Subject.String(),
		"issuer":        cert.Issuer.String(),
		"notBefore":     cert.NotBefore.Format(time.RFC3339),
		"notAfter":      cert.NotAfter.Format(time.RFC3339),
		"serialNumber":  cert.SerialNumber.String(),
		"fingerprint":   hex.EncodeToString(fp[:]),
		"hasPrivateKey": hasKey,
	}
}

// smimeP12Password derives the at-rest PKCS#12 password from the server secret.
// The uploaded key is stored ENCRYPTED under this (never in plaintext); because
// the secret lives in config, the server can use the key without a per-user
// passphrase — the model the SPA's no-passphrase upload implies. Adding a
// per-session passphrase (the old webmail's stronger model) is a future option.
func smimeP12Password(secret []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("smime-p12-at-rest-v1"))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// unlockSmimeIdentity returns the caller's stored S/MIME private key and
// certificate, or ok=false when none is set or it cannot be decrypted.
func unlockSmimeIdentity(st *objectstore.Store, secret []byte) (crypto.PrivateKey, *x509.Certificate, bool) {
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok || len(id.P12) == 0 {
		return nil, nil, false
	}
	key, cert, err := smime.ParseIdentity(id.P12, smimeP12Password(secret))
	if err != nil {
		return nil, nil, false
	}
	return key, cert, true
}

// handleGetSmimeCert returns the caller's stored S/MIME certificate details, read
// from the same store property the old webmail uses. Returns {hasKeys:false} when
// none is set.
func (s *Server) handleGetSmimeCert(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok {
		writeJSON(w, http.StatusOK, map[string]any{"hasKeys": false})
		return
	}
	cert, err := x509.ParseCertificate(id.Cert)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"hasKeys": false})
		return
	}
	writeJSON(w, http.StatusOK, certInfo(cert, len(id.P12) > 0))
}

// handleUploadSmimeCert stores an uploaded S/MIME identity. The SPA sends a
// PEM certificate and private key; they are validated as a matching pair, then
// stored as a PKCS#12 encrypted at rest under a server-derived password (see
// smimeP12Password). The key is never persisted in plaintext.
func (s *Server) handleUploadSmimeCert(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	pair, err := tls.X509KeyPair([]byte(req.Cert), []byte(req.Key))
	if err != nil || len(pair.Certificate) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the certificate and key are invalid or do not match"})
		return
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid certificate"})
		return
	}
	p12, err := pkcs12.Modern.Encode(pair.PrivateKey, cert, nil, smimeP12Password(s.secret))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the identity"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{P12: p12, Cert: cert.Raw}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the identity"})
		return
	}
	writeJSON(w, http.StatusOK, certInfo(cert, true))
}

// handleDeleteSmimeCert removes the caller's stored S/MIME identity.
func (s *Server) handleDeleteSmimeCert(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	if err := st.ClearSmimeIdentity(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove certificate"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
