package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// GetDomainCatchAll returns the address of the account a domain collects unknown
// recipients in, found=false when the domain has none or names an account that no longer
// exists.
func (d *SQLDirectory) GetDomainCatchAll(domain string) (address string, found bool, err error) {
	err = d.db.QueryRow(`
SELECT u.username FROM domains d
  JOIN users u ON u.id = d.catchall_user_id
 WHERE d.domainname = ?`, strings.ToLower(strings.TrimSpace(domain))).Scan(&address)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return address, true, nil
}

// SetDomainCatchAll names the account a domain collects unknown recipients in. An empty
// address clears it, returning the domain to refusing an unknown recipient.
//
// The account must be a mailbox user IN THAT DOMAIN. A catch-all in another domain would
// pour one tenant's unknown-recipient mail into another tenant's mailbox, which is the
// cross-tenant leak this directory keeps out everywhere else.
func (d *SQLDirectory) SetDomainCatchAll(domain, address string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	address = strings.ToLower(strings.TrimSpace(address))
	domainID, ok, err := d.DomainID(domain)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("directory: domain %q not found", domain)
	}
	if address == "" {
		_, err = d.db.Exec(`UPDATE domains SET catchall_user_id = NULL WHERE id = ?`, domainID)
		return err
	}
	userID, err := d.catchAllCandidate(address, domainID)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`UPDATE domains SET catchall_user_id = ? WHERE id = ?`, userID, domainID)
	return err
}

// catchAllCandidate resolves an address to the account id it may be set as a catch-all
// under, refusing an address that is not a mailbox user of that domain.
func (d *SQLDirectory) catchAllCandidate(address string, domainID int64) (int64, error) {
	var id int64
	var displayType int
	var rowDomain int64
	err := d.db.QueryRow(
		`SELECT id, display_type, domain_id FROM users WHERE username = ?`, address).Scan(&id, &displayType, &rowDomain)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("directory: %q is not an account", address)
	}
	if err != nil {
		return 0, err
	}
	if displayType != dtMailuser {
		return 0, fmt.Errorf("directory: %q is not a mailbox account", address)
	}
	if rowDomain != domainID {
		return 0, fmt.Errorf("directory: %q is not in this domain", address)
	}
	return id, nil
}

// ResolveCatchAll maps an address no account owns to its domain's catch-all mailbox,
// applying the same acceptance checks Resolve does: the domain is active, the account can
// receive, and it has a mailbox. It is used by delivery only; authentication and every
// other address lookup stay exact.
func (d *SQLDirectory) ResolveCatchAll(address string) (string, bool) {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		return "", false
	}
	var maildir string
	var addrStatus, domainStatus int
	err := d.db.QueryRow(`
SELECT u.maildir, u.address_status, dm.domain_status
  FROM domains dm
  JOIN users u ON u.id = dm.catchall_user_id
 WHERE dm.domainname = ?`, address[at+1:]).Scan(&maildir, &addrStatus, &domainStatus)
	if err != nil || maildir == "" || domainStatus != 0 {
		return "", false
	}
	u := addrStatus & afUserMask
	if (u != afUserNormal && u != afUserSharedMbox) || addrStatus&afDomainMask != 0 {
		return "", false
	}
	return d.storePath(maildir), true
}
