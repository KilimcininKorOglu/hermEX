package directory

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// prDisplayName is PR_DISPLAY_NAME (PtUnicode): the user_properties row (order_id
// 1) a contact's friendly name lives in, the same row SearchGAL reads the
// display name from, falling back to the address when it is absent.
const prDisplayName = 0x3001001F

// ContactInfo is an org mail contact's administrative summary: its GAL address,
// display name, the local domain it is filed under, and the LDAP identity it was
// synced from. LDAPID is the hex form of the directory object's stable id and is
// empty for a contact an operator created by hand.
type ContactInfo struct {
	Address     string
	DisplayName string
	Domain      string
	LDAPID      string
}

// ErrLDAPMasteredContact refuses a manual edit of a contact the LDAP downsync owns.
// The next sync would overwrite the change, so accepting it would report a success
// that does not survive.
var ErrLDAPMasteredContact = errors.New("directory: this contact is mastered by the LDAP directory")

// CreateContact creates an organizational mail contact: a users row
// (display_type = DT_REMOTE_MAILUSER, no password or maildir, so it cannot log in
// and owns no mailbox) filed under an existing local domain, plus its
// PR_DISPLAY_NAME property when a name is given, in one transaction. The address
// is the GAL address users see and send to, typically external; it must be an
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

// UpdateContact changes an org mail contact's display name (PR_DISPLAY_NAME),
// identified by its address; the address and filing domain are immutable (to
// change them, delete and re-add). An empty name clears the property so the GAL
// falls back to the address. It reports whether a contact was found; a row that
// is not a contact (display_type ≠ DT_REMOTE_MAILUSER) is left untouched.
func (d *SQLDirectory) UpdateContact(email, displayName string) (bool, error) {
	id, found, err := d.editableContactID(email)
	if err != nil || !found {
		return false, err
	}
	return d.setContactDisplayName(id, displayName)
}

// editableContactID resolves an address to a contact an operator may change: it reports
// found=false for an address that is not a contact, and refuses one the LDAP downsync
// owns, which is the single place both manual edit paths take that decision.
func (d *SQLDirectory) editableContactID(email string) (id int64, found bool, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	err = d.db.QueryRow(
		`SELECT id FROM users WHERE username = ? AND display_type = ?`, email, dtContact).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	mastered, err := d.ldapMasteredContact(id)
	if err != nil {
		return 0, false, err
	}
	if mastered {
		return 0, false, ErrLDAPMasteredContact
	}
	return id, true, nil
}

// setContactDisplayName writes or clears a contact's PR_DISPLAY_NAME. Clearing it lets the
// GAL fall back to the address.
func (d *SQLDirectory) setContactDisplayName(id int64, displayName string) (bool, error) {
	var err error
	if name := strings.TrimSpace(displayName); name != "" {
		_, err = d.db.Exec(
			`INSERT INTO user_properties (user_id, proptag, order_id, propval_str, propval_bin) VALUES (?, ?, 1, ?, NULL)
			 ON DUPLICATE KEY UPDATE propval_str = VALUES(propval_str), propval_bin = NULL`,
			id, prDisplayName, name)
		return true, err
	}
	_, err = d.db.Exec(
		`DELETE FROM user_properties WHERE user_id = ? AND proptag = ? AND order_id = 1`,
		id, prDisplayName)
	return true, err
}

// DeleteContact removes an org mail contact, reporting whether one was removed.
// Deleting its users row cascades to its user_properties. A contact the LDAP downsync
// owns is refused: the next sync would recreate it.
func (d *SQLDirectory) DeleteContact(email string) (bool, error) {
	id, found, err := d.editableContactID(email)
	if err != nil || !found {
		return false, err
	}
	_, err = d.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err == nil, err
}

// contactSelect reads a contact's administrative summary: the address, the display-name
// property, the filing domain and the LDAP identity. Both listings share it so a column
// added here reaches every contact surface.
const contactSelect = `
SELECT u.username, dn.propval_str, dm.domainname, u.externid
  FROM users u
  JOIN domains dm ON dm.id = u.domain_id
  LEFT JOIN user_properties dn ON dn.user_id = u.id AND dn.proptag = ? AND dn.order_id = 1
 WHERE u.display_type = ?`

// ListContacts returns every org mail contact, ordered by address, for the admin
// API. DisplayName is PR_DISPLAY_NAME (order_id 1) when set, else the address.
func (d *SQLDirectory) ListContacts() ([]ContactInfo, error) {
	rows, err := d.db.Query(contactSelect+` ORDER BY u.username`, prDisplayName, dtContact)
	if err != nil {
		return nil, err
	}
	return scanContacts(rows)
}

// ListContactsInDomain returns one domain's mail contacts, ordered by address,
// for the per-domain admin views.
func (d *SQLDirectory) ListContactsInDomain(domainID int64) ([]ContactInfo, error) {
	rows, err := d.db.Query(contactSelect+` AND u.domain_id = ? ORDER BY u.username`,
		prDisplayName, dtContact, domainID)
	if err != nil {
		return nil, err
	}
	return scanContacts(rows)
}

// scanContacts reads a contactSelect result set, falling the display name back to the
// address and rendering the LDAP id as hex for display.
func scanContacts(rows *sql.Rows) ([]ContactInfo, error) {
	defer rows.Close()
	var out []ContactInfo
	for rows.Next() {
		var c ContactInfo
		var name sql.NullString
		var externid []byte
		if err := rows.Scan(&c.Address, &name, &c.Domain, &externid); err != nil {
			return nil, err
		}
		c.DisplayName = c.Address
		if name.Valid && name.String != "" {
			c.DisplayName = name.String
		}
		c.LDAPID = hex.EncodeToString(externid)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertLDAPContact records a mail contact discovered in an LDAP downsync: an existing
// contact (matched by address) has its LDAP id and display name refreshed, and a missing
// one is created filed under the given local domain. A contact's own address is external,
// so the filing domain comes from the sync configuration rather than from the address. An
// address that already belongs to something other than a contact (a mailbox user, a
// distribution list) is refused rather than converted, because converting a mailbox
// account into a GAL entry would take its mailbox away. It reports whether a contact was
// created.
func (d *SQLDirectory) UpsertLDAPContact(email string, externid []byte, displayName, domain string) (created bool, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	id, found, err := d.contactRowID(email)
	if err != nil {
		return false, err
	}
	if !found {
		if id, err = d.CreateContact(email, displayName, domain); err != nil {
			return false, err
		}
		created = true
	}
	if _, err := d.db.Exec(`UPDATE users SET externid = ? WHERE id = ?`, externid, id); err != nil {
		return false, err
	}
	if created {
		return true, nil
	}
	_, err = d.setContactDisplayName(id, displayName)
	return false, err
}

// contactRowID resolves an address to its contact row, refusing an address that exists as
// something other than a contact.
func (d *SQLDirectory) contactRowID(email string) (id int64, found bool, err error) {
	var displayType int
	err = d.db.QueryRow(`SELECT id, display_type FROM users WHERE username = ?`, email).Scan(&id, &displayType)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if displayType != dtContact {
		return 0, false, fmt.Errorf("directory: %q already exists and is not a mail contact", email)
	}
	return id, true, nil
}

// DeleteLDAPContact removes a contact the downsync owns, used when it disappears from the
// LDAP directory. A contact with no LDAP id is left alone, so a hand-made contact is never
// pruned by a sync.
func (d *SQLDirectory) DeleteLDAPContact(email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	res, err := d.db.Exec(
		`DELETE FROM users WHERE username = ? AND display_type = ? AND externid IS NOT NULL`, email, dtContact)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ldapMasteredContact reports whether a contact row carries an LDAP id.
func (d *SQLDirectory) ldapMasteredContact(id int64) (bool, error) {
	var externid []byte
	if err := d.db.QueryRow(`SELECT externid FROM users WHERE id = ?`, id).Scan(&externid); err != nil {
		return false, err
	}
	return len(externid) > 0, nil
}
