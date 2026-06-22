package directory

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DKIMKeyInfo is a domain's DKIM key metadata for display — never the private key: the
// selector, the TXT record value to publish, and whether signing is enabled.
type DKIMKeyInfo struct {
	Selector  string
	PublicTXT string
	Enabled   bool
}

// DKIMKey returns the PEM private key and selector of a domain's ENABLED signing key,
// for the outbound signer. found is false when the domain has no key or its key is not
// enabled, so a generated-but-unpublished key never signs. It satisfies
// dkimsign.KeyProvider.
func (d *SQLDirectory) DKIMKey(domain string) (privPEM []byte, selector string, found bool, err error) {
	var pk, sel string
	err = d.db.QueryRow(
		`SELECT private_key, selector FROM dkim_keys WHERE domain = ? AND enabled = 1`,
		strings.ToLower(domain)).Scan(&pk, &sel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return []byte(pk), sel, true, nil
}

// SetDKIMKey stores or replaces a domain's signing key. It is always stored DISABLED:
// the operator must publish the DNS record and then enable it, so generating (or
// regenerating) a key never starts producing DKIM=fail.
func (d *SQLDirectory) SetDKIMKey(domain, selector string, privPEM []byte, publicTXT string) error {
	_, err := d.db.Exec(
		`INSERT INTO dkim_keys (domain, selector, private_key, public_txt, enabled, created_at)
		 VALUES (?, ?, ?, ?, 0, ?)
		 ON DUPLICATE KEY UPDATE selector = VALUES(selector), private_key = VALUES(private_key),
		   public_txt = VALUES(public_txt), enabled = 0, created_at = VALUES(created_at)`,
		strings.ToLower(domain), selector, string(privPEM), publicTXT, time.Now().UnixMilli())
	return err
}

// SetDKIMEnabled turns a domain's outbound signing on or off.
func (d *SQLDirectory) SetDKIMEnabled(domain string, enabled bool) error {
	_, err := d.db.Exec(`UPDATE dkim_keys SET enabled = ? WHERE domain = ?`, enabled, strings.ToLower(domain))
	return err
}

// GetDKIMKeyInfo returns a domain's key metadata for display (never the private key).
func (d *SQLDirectory) GetDKIMKeyInfo(domain string) (DKIMKeyInfo, bool, error) {
	var info DKIMKeyInfo
	err := d.db.QueryRow(
		`SELECT selector, public_txt, enabled FROM dkim_keys WHERE domain = ?`,
		strings.ToLower(domain)).Scan(&info.Selector, &info.PublicTXT, &info.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return DKIMKeyInfo{}, false, nil
	}
	if err != nil {
		return DKIMKeyInfo{}, false, err
	}
	return info, true, nil
}

// DeleteDKIMKey removes a domain's signing key.
func (d *SQLDirectory) DeleteDKIMKey(domain string) error {
	_, err := d.db.Exec(`DELETE FROM dkim_keys WHERE domain = ?`, strings.ToLower(domain))
	return err
}
