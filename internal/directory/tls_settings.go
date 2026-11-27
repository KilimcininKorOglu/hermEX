package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"time"
)

// TLSSettings is the operator's choice of how the front door obtains its serving
// certificate. Mode is "manual" (operator-uploaded certs in tls_certs, the Phase-2
// default) or "acme" (the gateway obtains and renews Let's Encrypt certificates
// automatically). The ACME fields are read only in "acme" mode: ACMEEmail is the
// account contact, ACMECAURL overrides the CA directory (empty = the CertMagic
// default; a staging or pebble URL in non-production), and ACMEAgreed records that
// the operator accepted the CA's terms of service.
type TLSSettings struct {
	Mode       string
	ACMEEmail  string
	ACMECAURL  string
	ACMEAgreed bool
}

// GetTLSSettings returns the stored TLS-mode settings and whether a row exists. When
// none has been saved, found is false and the caller treats it as manual mode, the
// safe default that needs no ACME account, so an upgrade never silently starts
// reaching out to a CA.
func (d *SQLDirectory) GetTLSSettings() (TLSSettings, bool, error) {
	var s TLSSettings
	var agreed int
	err := d.db.QueryRow(
		`SELECT mode, acme_email, acme_ca_url, acme_agreed FROM tls_settings WHERE id = 1`).
		Scan(&s.Mode, &s.ACMEEmail, &s.ACMECAURL, &agreed)
	if errors.Is(err, sql.ErrNoRows) {
		return TLSSettings{Mode: "manual"}, false, nil
	}
	if err != nil {
		return TLSSettings{}, false, err
	}
	s.ACMEAgreed = agreed != 0
	return s, true, nil
}

// SetTLSSettings persists the TLS-mode settings, upserting the single row. The
// gateway reads them at startup to choose its certificate source; switching mode is
// structural and takes effect on the next gateway start.
func (d *SQLDirectory) SetTLSSettings(s TLSSettings) error {
	if err := validTLSSettings(s); err != nil {
		return err
	}
	agreed := 0
	if s.ACMEAgreed {
		agreed = 1
	}
	_, err := d.db.Exec(
		`INSERT INTO tls_settings (id, mode, acme_email, acme_ca_url, acme_agreed, updated_at)
		 VALUES (1, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE mode = VALUES(mode), acme_email = VALUES(acme_email),
		   acme_ca_url = VALUES(acme_ca_url), acme_agreed = VALUES(acme_agreed),
		   updated_at = VALUES(updated_at)`,
		s.Mode, s.ACMEEmail, s.ACMECAURL, agreed, time.Now().UnixMilli())
	return err
}

// validTLSSettings refuses a mode the gateway does not know, and an ACME mode
// without the contact address and accepted terms a CA account needs.
func validTLSSettings(s TLSSettings) error {
	switch s.Mode {
	case "manual":
		return nil
	case "acme":
		if s.ACMEEmail == "" || !s.ACMEAgreed {
			return errors.New("directory: ACME mode needs a contact email and the accepted terms")
		}
		return validACMEAccount(s)
	}
	return errors.New("directory: the TLS mode must be manual or acme")
}

// validACMEAccount refuses a contact that is not one bare addr-spec (RFC 8555
// section 7.3 sends it as a mailto: URL) and a directory URL that is not HTTPS
// with a host (section 6.1: every ACME request is HTTPS). An empty URL is the
// issuer's default CA.
func validACMEAccount(s TLSSettings) error {
	if a, err := mail.ParseAddress(s.ACMEEmail); err != nil || a.Name != "" || a.Address != s.ACMEEmail {
		return fmt.Errorf("directory: ACME contact %q is not one email address", s.ACMEEmail)
	}
	if s.ACMECAURL == "" {
		return nil
	}
	if u, err := url.Parse(s.ACMECAURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("directory: ACME directory URL %q is not an https URL", s.ACMECAURL)
	}
	return nil
}
