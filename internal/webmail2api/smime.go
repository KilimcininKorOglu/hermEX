package webmail2api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"time"

	"hermex/internal/objectstore"
)

// handleGetSmimeCert returns the caller's stored S/MIME certificate details, read
// from the same store property the old webmail uses (so an identity uploaded
// there is visible here too). It is model-independent — it never touches how the
// private key is stored. Returns {hasKeys:false} when none is set.
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
	fp := sha256.Sum256(cert.Raw)
	writeJSON(w, http.StatusOK, map[string]any{
		"subject":       cert.Subject.String(),
		"issuer":        cert.Issuer.String(),
		"notBefore":     cert.NotBefore.Format(time.RFC3339),
		"notAfter":      cert.NotAfter.Format(time.RFC3339),
		"serialNumber":  cert.SerialNumber.String(),
		"fingerprint":   hex.EncodeToString(fp[:]),
		"hasPrivateKey": len(id.P12) > 0,
	})
}

// handleDeleteSmimeCert removes the caller's stored S/MIME identity. Like the
// read, it is model-independent.
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

// handleUploadSmimeCert is intentionally not wired to storage yet: the SPA uploads
// a bare cert+key PEM with no passphrase, which conflicts with the store's
// encrypted-PKCS#12 + per-session-passphrase model. Choosing the at-rest
// key-storage posture is a security decision left to the operator, so this
// reports a clear pending state rather than silently picking one.
func (s *Server) handleUploadSmimeCert(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "S/MIME key upload is pending the at-rest key-storage decision; view and remove are available",
	})
}
