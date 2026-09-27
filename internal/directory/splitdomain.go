package directory

import (
	"database/sql"
	"errors"
	"strings"
)

// SplitRelayHost returns the host that serves a split domain's addresses without
// a mailbox here, or "" when the domain is not split or unknown.
func (d *SQLDirectory) SplitRelayHost(domain string) (string, error) {
	var host string
	err := d.db.QueryRow(`SELECT split_relay_host FROM domains WHERE domainname = ?`,
		strings.ToLower(strings.TrimSpace(domain))).Scan(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return host, err
}

// SetSplitRelayHost stores a domain's split relay host; "" turns splitting off.
func (d *SQLDirectory) SetSplitRelayHost(domain, host string) error {
	_, err := d.db.Exec(`UPDATE domains SET split_relay_host = ? WHERE domainname = ?`,
		strings.ToLower(strings.TrimSpace(host)), strings.ToLower(strings.TrimSpace(domain)))
	return err
}
