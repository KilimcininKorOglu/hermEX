package directory

import (
	"database/sql"
	"errors"
	"strings"
)

// GetDomainAVScan reports whether antivirus scanning is enabled for a domain's
// inbound and outbound mail. A domain with no row returns (false, false): AV is
// opt-in per tenant.
func (d *SQLDirectory) GetDomainAVScan(domain string) (inbound, outbound bool, err error) {
	err = d.db.QueryRow(
		`SELECT av_scan_inbound, av_scan_outbound FROM domains WHERE domainname = ?`,
		strings.ToLower(strings.TrimSpace(domain))).Scan(&inbound, &outbound)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return inbound, outbound, err
}

// SetDomainAVScan sets a domain's inbound and outbound antivirus scanning toggles.
func (d *SQLDirectory) SetDomainAVScan(domain string, inbound, outbound bool) error {
	_, err := d.db.Exec(
		`UPDATE domains SET av_scan_inbound = ?, av_scan_outbound = ? WHERE domainname = ?`,
		inbound, outbound, strings.ToLower(strings.TrimSpace(domain)))
	return err
}
