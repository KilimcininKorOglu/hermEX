package webmail2api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// handleRecipientCert returns a recipient's published S/MIME public certificate
// (PEM) so the SPA can encrypt to them. The certificate is public, so any
// authenticated user may fetch it; the result is null when the recipient (a local
// user) has published none or is not local.
func (s *Server) handleRecipientCert(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}
	maildir, ok := s.accounts.Resolve(address)
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	defer st.Close()
	id, ok, err := st.GetSmimeIdentity()
	if err != nil || !ok || len(id.Cert) == 0 {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.Cert})
	writeJSON(w, http.StatusOK, map[string]string{"cert": string(pemBytes)})
}

// In the client-side S/MIME model the private key never reaches the server: it is
// imported, stored, and used (sign/encrypt/verify/decrypt) entirely in the
// browser. The server only keeps the user's PUBLIC certificate, so it can be
// published to the directory/GAL for others to encrypt to.

// certEmail returns a certificate's email address (its SAN, else its common
// name), used to label the verified signer.
func certEmail(cert *x509.Certificate) string {
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	return cert.Subject.CommonName
}

// certInfo projects an x509 certificate into the SPA's SMIMECertInfo shape.
func certInfo(cert *x509.Certificate) map[string]any {
	fp := sha256.Sum256(cert.Raw)
	return map[string]any{
		"subject":      cert.Subject.String(),
		"issuer":       cert.Issuer.String(),
		"notBefore":    cert.NotBefore.Format(time.RFC3339),
		"notAfter":     cert.NotAfter.Format(time.RFC3339),
		"serialNumber": cert.SerialNumber.String(),
		"fingerprint":  hex.EncodeToString(fp[:]),
	}
}

// handleGetSmimeCert returns the caller's published S/MIME public certificate, or
// {hasKeys:false} when none is published.
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
	if err != nil || !ok || len(id.Cert) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"hasKeys": false})
		return
	}
	cert, err := x509.ParseCertificate(id.Cert)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"hasKeys": false})
		return
	}
	writeJSON(w, http.StatusOK, certInfo(cert))
}

// handleUploadSmimeCert publishes the caller's S/MIME PUBLIC certificate (PEM or
// DER). The matching private key stays in the browser and is never sent — a
// request carrying a private key is rejected.
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
	if req.Key != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send only the public certificate; the private key stays in your browser"})
		return
	}
	der, err := parseCertDER([]byte(req.Cert))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid certificate"})
		return
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid certificate"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Cert: cert.Raw}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not publish the certificate"})
		return
	}
	writeJSON(w, http.StatusOK, certInfo(cert))
}

// handleDeleteSmimeCert removes the caller's published certificate.
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

// parseCertDER accepts a certificate as PEM or raw DER and returns its DER bytes.
func parseCertDER(data []byte) ([]byte, error) {
	if block, _ := pem.Decode(data); block != nil {
		return block.Bytes, nil
	}
	// Already DER (or invalid — ParseCertificate by the caller decides).
	if _, err := x509.ParseCertificate(data); err != nil {
		return nil, err
	}
	return data, nil
}
