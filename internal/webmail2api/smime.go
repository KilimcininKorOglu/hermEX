package webmail2api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/smime"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
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
	info := certInfo(cert)
	info["mode"] = id.Mode
	writeJSON(w, http.StatusOK, info)
}

// handleUploadSmimeCert publishes the caller's S/MIME PUBLIC certificate (PEM or
// DER). The matching private key stays in the browser and is never sent; a
// request carrying a private key is rejected.
func (s *Server) handleUploadSmimeCert(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		Mode       string `json:"mode"`       // "server" stores the key here; otherwise browser mode
		Cert       string `json:"cert"`       // browser mode: the PUBLIC certificate (PEM/DER)
		Key        string `json:"key"`        // never accepted: a raw key must not be sent
		P12        string `json:"p12"`        // server mode: the PKCS#12, base64
		Passphrase string `json:"passphrase"` // server mode: the .p12 password
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if req.Key != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "do not send a raw private key; use a .p12 for server-side storage, or only the certificate for browser storage"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()

	// Server mode: open the .p12 with the user's password, then re-encrypt it at
	// rest under a server-derived password so the server can sign/decrypt for them.
	if req.Mode == "server" || req.P12 != "" {
		p12Bytes, derr := base64.StdEncoding.DecodeString(req.P12)
		if derr != nil || len(p12Bytes) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid .p12 file"})
			return
		}
		key, cert, perr := smime.ParseIdentity(p12Bytes, req.Passphrase)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "wrong password or unreadable .p12"})
			return
		}
		reP12, eerr := pkcs12.Modern.Encode(key, cert, nil, smimeP12Password(s.secret))
		if eerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the identity"})
			return
		}
		if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Mode: "server", Cert: cert.Raw, P12: reP12}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the identity"})
			return
		}
		writeJSON(w, http.StatusOK, certInfo(cert))
		return
	}

	// Browser mode: publish only the public certificate; the key stays in the browser.
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
	if err := st.SetSmimeIdentity(objectstore.SmimeIdentity{Mode: "browser", Cert: cert.Raw}); err != nil {
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
	// Already DER (or invalid; ParseCertificate by the caller decides).
	if _, err := x509.ParseCertificate(data); err != nil {
		return nil, err
	}
	return data, nil
}
