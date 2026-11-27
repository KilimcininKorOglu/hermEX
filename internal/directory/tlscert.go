package directory

import (
	"slices"
	"strings"
	"time"

	"hermex/internal/logging"
)

// TLSCertData is a stored serving certificate including its private key, loaded
// in-process by the certificate provider that presents it on a TLS listener. The
// key material here is a secret: it is never returned by the admin API or logged.
type TLSCertData struct {
	Name    string // SNI host the cert serves; "" is the default
	CertPEM string // full chain PEM
	KeyPEM  string // private key PEM (secret)
}

// TLSCertInfo is a stored certificate's metadata for display, never the key:
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
	// The database is the only copy of this key, so it is wrapped at rest when a
	// key secret is configured; without one it is stored as before.
	sealed, err := d.wrapKey(wrapTLS, keyPEM)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(
		`INSERT INTO tls_certs (name, cert_pem, key_pem, not_after, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE cert_pem = VALUES(cert_pem), key_pem = VALUES(key_pem),
		   not_after = VALUES(not_after), updated_at = VALUES(updated_at)`,
		strings.ToLower(name), certPEM, sealed, notAfter, time.Now().UnixMilli())
	return err
}

// LoadTLSCerts returns every stored certificate including its private key, for the
// in-process certificate provider to parse into a serving snapshot. It is not an
// admin-facing call, ListTLSCerts returns the display metadata without keys.
func (d *SQLDirectory) LoadTLSCerts() (out []TLSCertData, err error) {
	rows, err := d.db.Query(`SELECT name, cert_pem, key_pem FROM tls_certs`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	var plaintext []string // rows still stored unwrapped, rewritten after the scan
	for rows.Next() {
		var c TLSCertData
		if err := rows.Scan(&c.Name, &c.CertPEM, &c.KeyPEM); err != nil {
			return nil, err
		}
		u, err := d.unwrapKey(wrapTLS, c.KeyPEM)
		if err != nil {
			return nil, err
		}
		c.KeyPEM = u.plain
		if d.rewrapNeeded(u.wrapped) {
			plaintext = append(plaintext, c.Name)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.rewrapTLSCerts(out, plaintext)
	return out, nil
}

// rewrapTLSCerts writes plaintext serving keys back wrapped after the scan closes.
// Best-effort: a failed rewrite is recorded but never stops TLS serving.
func (d *SQLDirectory) rewrapTLSCerts(certs []TLSCertData, plaintext []string) {
	for _, c := range certs {
		if !slices.Contains(plaintext, c.Name) {
			continue
		}
		sealed, err := d.wrapKey(wrapTLS, c.KeyPEM)
		if err != nil {
			d.warn("directory.tlscert_rewrap.fail", err, logging.Fields{"name": c.Name})
			continue
		}
		if _, err := d.db.Exec(`UPDATE tls_certs SET key_pem = ? WHERE name = ?`, sealed, c.Name); err != nil {
			d.warn("directory.tlscert_rewrap.fail", err, logging.Fields{"name": c.Name})
		}
	}
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
func (d *SQLDirectory) ListTLSCerts() (out []TLSCertInfo, err error) {
	rows, err := d.db.Query(`SELECT name, not_after, updated_at FROM tls_certs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
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
