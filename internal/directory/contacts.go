package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// prDisplayName is PR_DISPLAY_NAME (PtUnicode): the user_properties row (order_id
// 1) a contact's friendly name lives in — the same row SearchGAL reads the
// display name from, falling back to the address when it is absent.
const prDisplayName = 0x3001001F

// ContactInfo is an org mail contact's administrative summary: its GAL address,
// display name, and the local domain it is filed under.
type ContactInfo struct {
	Address     string
	DisplayName string
	Domain      string
}

// CreateContact creates an organizational mail contact: a users row
// (display_type = DT_REMOTE_MAILUSER, no password or maildir, so it cannot log in
// and owns no mailbox) filed under an existing local domain, plus its
// PR_DISPLAY_NAME property when a name is given, in one transaction. The address
// is the GAL address users see and send to — typically external; it must be an
// email and unused. The filing domain must already exist; because the GAL is
// org-wide, it only scopes which active domain the contact rides on, not who can
// see it.
func (d *SQLDirectory) CreateContact(email, displayName, domain string) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if at := strings.LastIndexByte(email, '@'); at <= 0 {
		return 0, errors.New("directory: contact address must be an email address")
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	var domainID int64
	err := d.db.QueryRow(`SELECT id FROM domains WHERE domainname = ?`, domain).Scan(&domainID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("directory: domain %q not found", domain)
	}
	if err != nil {
		return 0, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO users (username, domain_id, display_type, password, maildir) VALUES (?, ?, ?, '', '')`,
		email, domainID, dtContact)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if name := strings.TrimSpace(displayName); name != "" {
		if _, err := tx.Exec(
			`INSERT INTO user_properties (user_id, proptag, order_id, propval_str) VALUES (?, ?, 1, ?)`,
			id, prDisplayName, name); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// DeleteContact removes an org mail contact, reporting whether one was removed.
// Deleting its users row cascades to its user_properties.
func (d *SQLDirectory) DeleteContact(email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	res, err := d.db.Exec(`DELETE FROM users WHERE username = ? AND display_type = ?`, email, dtContact)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListContacts returns every org mail contact, ordered by address, for the admin
// API. DisplayName is PR_DISPLAY_NAME (order_id 1) when set, else the address.
func (d *SQLDirectory) ListContacts() ([]ContactInfo, error) {
	rows, err := d.db.Query(`
SELECT u.username, dn.propval_str, dm.domainname
  FROM users u
  JOIN domains dm ON dm.id = u.domain_id
  LEFT JOIN user_properties dn ON dn.user_id = u.id AND dn.proptag = ? AND dn.order_id = 1
 WHERE u.display_type = ?
 ORDER BY u.username`, prDisplayName, dtContact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContactInfo
	for rows.Next() {
		var c ContactInfo
		var name sql.NullString
		if err := rows.Scan(&c.Address, &name, &c.Domain); err != nil {
			return nil, err
		}
		c.DisplayName = c.Address
		if name.Valid && name.String != "" {
			c.DisplayName = name.String
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
