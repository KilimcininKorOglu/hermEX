package webmail2api

import (
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
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/smime"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// applySmime signs and/or encrypts a built message before sending. Signing uses
// the caller's stored identity; encrypting requires a stored certificate for
// every recipient. Sign-then-encrypt is applied (the standard order).
func (s *Server) applySmime(mailbox string, raw []byte, recipients []string, sign, encrypt bool) ([]byte, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, errors.New("mailbox unavailable")
	}
	defer st.Close()
	inner := raw
	if sign {
		key, cert, ok := unlockSmimeIdentity(st, s.secret)
		if !ok {
			return nil, errors.New("no S/MIME identity is set up; upload one in Settings")
		}
		signed, err := smime.Sign(inner, cert, key)
		if err != nil {
			return nil, fmt.Errorf("could not sign the message: %w", err)
		}
		inner = signed
	}
	if encrypt {
		certs := make([]*x509.Certificate, 0, len(recipients))
		for _, addr := range recipients {
			der, ok, _ := st.GetRecipientCert(addr)
			if !ok {
				return nil, fmt.Errorf("no S/MIME certificate stored for %s, so the message cannot be encrypted", addr)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, fmt.Errorf("the stored certificate for %s is invalid", addr)
			}
			certs = append(certs, cert)
		}
		enc, err := smime.Encrypt(inner, certs)
		if err != nil {
			return nil, fmt.Errorf("could not encrypt the message: %w", err)
		}
		inner = enc
	}
	return inner, nil
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
