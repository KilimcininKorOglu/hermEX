package directory

import (
	"database/sql"
	"errors"
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
// none has been saved, found is false and the caller treats it as manual mode — the
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
