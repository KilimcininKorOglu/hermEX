package admin

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
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
func (s *Server) tlsCertsPageData(r *http.Request, notice panelNotice) map[string]any {
	failed := readFailures{}
	data := map[string]any{"Nav": "tls", "CSRF": csrfCookieValue(r), "Notice": notice, "ReadFailed": failed}
	s.addTLSCertViews(data)
	settings, _, err := s.dir.GetTLSSettings()
	if s.noteRead(failed, "mode", "the certificate mode", err) {
		data["Mode"], data["ACMEEmail"] = settings.Mode, settings.ACMEEmail
		data["ACMECAURL"], data["ACMEAgreed"] = settings.ACMECAURL, settings.ACMEAgreed
	}
	sts, _, err := s.dir.GetMTASTSSettings()
	if s.noteRead(failed, "mtasts", "the MTA-STS settings", err) {
		data["MTASTSEnabled"], data["MTASTSMode"], data["MTASTSMaxAge"] = sts.Enabled, sts.Mode, sts.MaxAge
	}
	return data
}

// addTLSCertViews lists the stored certificates on a page-data map, or reports
// that they could not be read, which the table must not show as none stored.
func (s *Server) addTLSCertViews(data map[string]any) {
	infos, err := s.dir.ListTLSCerts()
	if err != nil {
		data["CertsError"] = s.notice("Could not read the stored certificates.", err)
		return
	}
	views := make([]tlsCertView, len(infos))
	for i, info := range infos {
		views[i] = tlsCertView{Name: info.Name, Expires: time.UnixMilli(info.NotAfter).UTC().Format("2006-01-02")}
	}
	data["Certs"] = views
}

// auditOld is the value an audit entry records as a setting's previous state: the
// stored value, or "unreadable" when reading it failed, so the entry never claims
// an empty previous value that was not stored.
func auditOld(v any, err error) any {
	if err != nil {
		return "unreadable"
	}
	return v
}

// handleUITLSSettings saves the certificate mode and ACME account settings. In acme
// mode the gateway obtains and renews Let's Encrypt certificates automatically; in
// manual mode it serves operator-uploaded certificates. Switching mode is structural,
// so it applies when the gateway next starts (the certificate contents still
// hot-reload without a restart).
func (s *Server) handleUITLSSettings(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	oldSettings, _, oldErr := s.dir.GetTLSSettings()
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
			s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, errorNotice("ACME mode needs an account email address.")))
			return
		}
		if !settings.ACMEAgreed {
			s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, errorNotice("ACME mode requires agreeing to the CA's terms of service.")))
			return
		}
		// A blank CA URL silently defaults to Let's Encrypt production, so a
		// misconfigured switch would burn the real rate limit across every tenant
		// name. Require an explicit directory URL so the choice of production vs.
		// staging is deliberate.
		if settings.ACMECAURL == "" {
			s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, errorNotice("ACME mode needs an explicit CA directory URL. Leaving it blank would default to Let's Encrypt production and risk its rate limit on a misconfiguration; paste the production or staging directory URL.")))
			return
		}
	}
	if err := s.dir.SetTLSSettings(settings); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, s.failNotice("Could not save the certificate mode.", err)))
		return
	}
	s.auditSettingChange(cl.Login, "tls_mode", logging.Fields{"old_mode": auditOld(oldSettings.Mode, oldErr), "new_mode": settings.Mode})
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, okNotice("Saved. Restart the gateway for a mode change to take effect.")))
}

// handleUIMTASTSSettings saves the MTA-STS publishing settings. When enabled, the
// gateway serves this server's policy at mta-sts.<domain> and the domain-detail page
// prescribes the records a domain owner publishes. Enforce mode is gated behind an
// explicit confirmation: a sender refuses delivery to a non-validated MX, so a
// certificate problem under enforce loses inbound mail, testing mode reports failures
// but still delivers, the safe default.
func (s *Server) handleUIMTASTSSettings(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	oldSTS, _, oldErr := s.dir.GetMTASTSSettings()
	enabled := r.FormValue("mtasts_enabled") == "on"
	mode := strings.ToLower(strings.TrimSpace(r.FormValue("mtasts_mode")))
	if mode != "enforce" && mode != "none" {
		mode = "testing"
	}
	maxAge, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("mtasts_max_age")))
	if maxAge <= 0 {
		maxAge = directory.MTASTSDefaultMaxAge
	}
	if enabled && mode == "enforce" && r.FormValue("mtasts_enforce_confirm") != "on" {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, errorNotice("Enforce mode makes senders refuse mail when the MX certificate does not validate. Tick the confirmation box to enable it, or use testing mode first.")))
		return
	}
	if err := s.dir.SetMTASTSSettings(directory.MTASTSSettings{Enabled: enabled, Mode: mode, MaxAge: maxAge}); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, s.failNotice("Could not save the MTA-STS settings.", err)))
		return
	}
	s.auditSettingChange(cl.Login, "mtasts", logging.Fields{
		"old_enabled": auditOld(oldSTS.Enabled, oldErr), "new_enabled": enabled,
		"old_mode": auditOld(oldSTS.Mode, oldErr), "new_mode": mode,
	})
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, okNotice("Saved MTA-STS publishing. Publish each domain's prescribed mta-sts and _mta-sts records (see the domain page); senders adopt a change on their next fetch.")))
}

// handleUITLSCerts renders the TLS-certificates page (system administrators only).
func (s *Server) handleUITLSCerts(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "tls_certs.html", s.tlsCertsPageData(r, panelNotice{}))
}

// handleUITLSCertUpload validates an uploaded certificate/key pair and stores it,
// keyed by an optional SNI name ("" is the default). The listeners pick it up on
// their next poll, so an operator's upload, or a renewal, applies without a
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
		// validateTLSCert describes the file the operator just pasted (not a pair,
		// unparseable leaf, already expired), not a server internal, and it is the
		// only thing telling them what to fix. It is shown rather than sanitized.
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, errorNotice("Upload rejected: "+err.Error())))
		return
	}
	if err := s.dir.SetTLSCert(name, certPEM, keyPEM, notAfter); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, s.failNotice("Could not store the certificate.", err)))
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
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, okNotice(fmt.Sprintf("Stored the %s certificate (%s). Listeners apply it within a minute, no restart.", label, covers))))
}

// handleUITLSCertDelete removes a stored certificate, after which the listeners
// fall back to the config-file certificate within a minute.
func (s *Server) handleUITLSCertDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	if err := s.dir.DeleteTLSCert(name); err != nil {
		s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, s.failNotice("Could not delete the certificate.", err)))
		return
	}
	s.render(w, "tls-certs-panel", s.tlsCertsPageData(r, okNotice("Certificate deleted. Listeners fall back to the config-file certificate within a minute.")))
}
