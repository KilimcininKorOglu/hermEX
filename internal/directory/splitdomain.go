package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
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

// SetSplitRelayHost stores a domain's split relay host; "" turns splitting off. A
// host the relay could not dial and an unknown domain are refused.
func (d *SQLDirectory) SetSplitRelayHost(domain, host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	domain = strings.ToLower(strings.TrimSpace(domain))
	if host != "" && !validHost(host) {
		return fmt.Errorf("directory: split relay host %q is neither a host name nor an IP address", host)
	}
	res, err := d.db.Exec(`UPDATE domains SET split_relay_host = ? WHERE domainname = ?`, host, domain)
	if err != nil {
		return err
	}
	found, err := d.updateFound(res, domainNameExistsQuery, domain)
	if err == nil && !found {
		err = fmt.Errorf("directory: no domain %q", domain)
	}
	return err
}

// validHost reports whether host is an IP address or an RFC 1123 host name: labels
// of 1-63 letters, digits and inner hyphens, 253 characters at most.
func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !validHostLabel(label) {
			return false
		}
	}
	return true
}

// validHostLabel reports whether one host-name label is well formed.
func validHostLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	return strings.Trim(label, hostLabelChars) == ""
}

// hostLabelChars is every character an RFC 1123 host-name label may hold.
const hostLabelChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-"
