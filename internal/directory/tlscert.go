package directory

import (
	"strings"
	"time"
)

// TLSCertData is a stored serving certificate including its private key, loaded
// in-process by the certificate provider that presents it on a TLS listener. The
// key material here is a secret: it is never returned by the admin API or logged.
type TLSCertData struct {
	Name    string // SNI host the cert serves; "" is the default
	CertPEM string // full chain PEM
	KeyPEM  string // private key PEM (secret)
}

// TLSCertInfo is a stored certificate's metadata for display — never the key:
// the SNI name, the leaf's expiry, and the version token of the last write.
type TLSCertInfo struct {
	Name      string
	NotAfter  int64 // leaf NotAfter, unix milliseconds
	UpdatedAt int64 // version token (unix milliseconds) of the last upsert
}

// SetTLSCert stores or replaces the serving certificate for name ("" is the
// default). notAfter is the leaf's expiry in unix milliseconds, recorded for
// display. updated_at is bumped so a polling provider reloads the new material.
func (d *SQLDirectory) SetTLSCert(name, certPEM, keyPEM string, notAfter int64) error {
	_, err := d.db.Exec(
		`INSERT INTO tls_certs (name, cert_pem, key_pem, not_after, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE cert_pem = VALUES(cert_pem), key_pem = VALUES(key_pem),
		   not_after = VALUES(not_after), updated_at = VALUES(updated_at)`,
		strings.ToLower(name), certPEM, keyPEM, notAfter, time.Now().UnixMilli())
	return err
}

// LoadTLSCerts returns every stored certificate including its private key, for the
// in-process certificate provider to parse into a serving snapshot. It is not an
// admin-facing call — ListTLSCerts returns the display metadata without keys.
func (d *SQLDirectory) LoadTLSCerts() ([]TLSCertData, error) {
	rows, err := d.db.Query(`SELECT name, cert_pem, key_pem FROM tls_certs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TLSCertData
	for rows.Next() {
		var c TLSCertData
		if err := rows.Scan(&c.Name, &c.CertPEM, &c.KeyPEM); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TLSCertVersion returns a cheap change probe for the certificate provider's poll:
// the newest updated_at across all rows and the row count. The provider reloads
// only when either changes, so a delete (which lowers the count) is detected even
// though it does not advance the max.
func (d *SQLDirectory) TLSCertVersion() (version, count int64, err error) {
	err = d.db.QueryRow(`SELECT COALESCE(MAX(updated_at), 0), COUNT(*) FROM tls_certs`).Scan(&version, &count)
	return version, count, err
}

// ListTLSCerts returns the stored certificates' display metadata (never the key),
// newest first, for the admin certificate page.
func (d *SQLDirectory) ListTLSCerts() ([]TLSCertInfo, error) {
	rows, err := d.db.Query(`SELECT name, not_after, updated_at FROM tls_certs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TLSCertInfo
	for rows.Next() {
		var i TLSCertInfo
		if err := rows.Scan(&i.Name, &i.NotAfter, &i.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteTLSCert removes the stored certificate for name ("" is the default).
func (d *SQLDirectory) DeleteTLSCert(name string) error {
	_, err := d.db.Exec(`DELETE FROM tls_certs WHERE name = ?`, strings.ToLower(name))
	return err
}
