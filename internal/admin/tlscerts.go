package admin

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"time"

	"hermex/internal/directory"
)

// validateTLSCert checks an uploaded certificate/key pair before it is stored: the
// key must match the certificate, the leaf must parse, and it must not already be
// expired. It returns the leaf's expiry (unix ms) and DNS names for display, so a
// bad upload is rejected at the panel rather than failing later on the listener.
func validateTLSCert(certPEM, keyPEM string) (notAfter int64, dnsNames []string, err error) {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return 0, nil, fmt.Errorf("the certificate and key are not a valid pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return 0, nil, fmt.Errorf("the leaf certificate could not be parsed: %w", err)
	}
	if time.Now().After(leaf.NotAfter) {
		return 0, nil, fmt.Errorf("the certificate expired on %s", leaf.NotAfter.UTC().Format("2006-01-02"))
	}
	return leaf.NotAfter.UnixMilli(), leaf.DNSNames, nil
}

// tlsCertView is a stored certificate's row for the panel: its SNI name (blank is
// the default) and a human expiry date.
type tlsCertView struct {
	Name    string
	Expires string
}

// tlsCertsPageData builds the TLS-certificates page model: the certificate mode and
// ACME account settings, the stored certificates, and a notice line.
func (s *Server) tlsCertsPageData(r *http.Request, notice string) map[string]any {
	infos, _ := s.dir.ListTLSCerts()
	views := make([]tlsCertView, len(infos))
	for i, info := range infos {
		views[i] = tlsCertView{Name: info.Name, Expires: time.UnixMilli(info.NotAfter).UTC().Format("2006-01-02")}
	}
	settings, _, _ := s.dir.GetTLSSettings()
	return map[string]any{
		"Nav": "tls", "CSRF": csrfCookieValue(r), "Certs": views, "Notice": notice,
		"Mode": settings.Mode, "ACMEEmail": settings.ACMEEmail,
		"ACMECAURL": settings.ACMECAURL, "ACMEAgreed": settings.ACMEAgreed,
	}
}

// handleUITLSSettings saves the certificate mode and ACME account settings. In acme
// mode the gateway obtains and renews Let's Encrypt certificates automatically; in
// manual mode it serves operator-uploaded certificates. Switching mode is structural,
// so it applies when the gateway next starts (the certificate contents still
// hot-reload without a restart).
func (s *Server) handleUITLSSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
	if mode != "acme" {
		mode = "manual"
	}
	settings := directory.TLSSettings{
		Mode:       mode,
		ACMEEmail:  strings.TrimSpace(r.FormValue("acme_email")),
		ACMECAURL:  strings.TrimSpace(r.FormValue("acme_ca_url")),
		ACMEAgreed: r.FormValue("acme_agreed") == "on",
	}
	if mode == "acme" {
		if settings.ACMEEmail == "" {
			s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "ACME mode needs an account email address."))
			return
		}
		if !settings.ACMEAgreed {
			s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "ACME mode requires agreeing to the CA's terms of service."))
			return
		}
		// A blank CA URL silently defaults to Let's Encrypt production, so a
		// misconfigured switch would burn the real rate limit across every tenant
		// name. Require an explicit directory URL so the choice of production vs.
		// staging is deliberate.
		if settings.ACMECAURL == "" {
			s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "ACME mode needs an explicit CA directory URL. Leaving it blank would default to Let's Encrypt production and risk its rate limit on a misconfiguration; paste the production or staging directory URL."))
			return
		}
	}
	if err := s.dir.SetTLSSettings(settings); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "Could not save the certificate mode: "+err.Error()))
		return
	}
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "Saved. Restart the gateway for a mode change to take effect."))
}

// handleUITLSCerts renders the TLS-certificates page (system administrators only).
func (s *Server) handleUITLSCerts(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "tls_certs.html", s.tlsCertsPageData(r, ""))
}

// handleUITLSCertUpload validates an uploaded certificate/key pair and stores it,
// keyed by an optional SNI name ("" is the default). The listeners pick it up on
// their next poll, so an operator's upload — or a renewal — applies without a
// restart.
func (s *Server) handleUITLSCertUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	certPEM := strings.TrimSpace(r.FormValue("cert")) + "\n"
	keyPEM := strings.TrimSpace(r.FormValue("key")) + "\n"
	notAfter, dnsNames, err := validateTLSCert(certPEM, keyPEM)
	if err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "Upload rejected: "+err.Error()))
		return
	}
	if err := s.dir.SetTLSCert(name, certPEM, keyPEM, notAfter); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "Could not store the certificate: "+err.Error()))
		return
	}
	label := name
	if label == "" {
		label = "default"
	}
	covers := "no SAN host names"
	if len(dnsNames) > 0 {
		covers = "covers " + strings.Join(dnsNames, ", ")
	}
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, fmt.Sprintf("Stored the %s certificate (%s). Listeners apply it within a minute, no restart.", label, covers)))
}

// handleUITLSCertDelete removes a stored certificate, after which the listeners
// fall back to the config-file certificate within a minute.
func (s *Server) handleUITLSCertDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	if err := s.dir.DeleteTLSCert(name); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "Could not delete the certificate: "+err.Error()))
		return
	}
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, "Certificate deleted. Listeners fall back to the config-file certificate within a minute."))
}
