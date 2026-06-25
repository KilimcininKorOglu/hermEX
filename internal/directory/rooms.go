package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// prRoomCapacity is PidTagAddressBookRoomCapacity (PtLong), the Exchange
// room-capacity address-book property. It is kept as a user_properties row
// (order_id 1) and surfaced to the webmail room picker; it is not yet exported
// over NSPI, so the exact wire form is internal for now. (prDisplayName lives
// alongside the contact helpers in this package.)
const prRoomCapacity = 0x08070003

// ListRooms returns the organization's bookable resource mailboxes - rooms and
// equipment (display_type DT_ROOM/DT_EQUIPMENT) - for the webmail room picker:
// enabled entries with a mailbox, ordered by address, each with its display name
// and (when set) seating capacity. The same active-user/active-domain filter
// SearchGAL applies is used so a disabled room never appears.
func (d *SQLDirectory) ListRooms() ([]GALEntry, error) {
	const q = `
SELECT u.username, u.display_type, COALESCE(dn.propval_str, ''), COALESCE(cap.propval_str, '')
  FROM users u JOIN domains d ON u.domain_id = d.id
  LEFT JOIN user_properties dn ON dn.user_id = u.id AND dn.proptag = ? AND dn.order_id = 1
  LEFT JOIN user_properties cap ON cap.user_id = u.id AND cap.proptag = ? AND cap.order_id = 1
 WHERE u.display_type IN (?, ?)
   AND u.maildir <> ''
   AND (u.address_status & ?) = ?
   AND (u.address_status & ?) = 0
   AND d.domain_status = 0
 ORDER BY u.username`
	rows, err := d.db.Query(q, prDisplayName, prRoomCapacity, dtRoom, dtEquipment, afUserMask, afUserNormal, afDomainMask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GALEntry
	for rows.Next() {
		var addr, name, capStr string
		var dt int
		if err := rows.Scan(&addr, &dt, &name, &capStr); err != nil {
			return nil, err
		}
		if name == "" {
			name = addr
		}
		capacity, _ := strconv.Atoi(capStr) // empty/non-numeric -> 0 (capacity unknown)
		out = append(out, GALEntry{DisplayName: name, Address: addr, DisplayType: dt, Capacity: capacity})
	}
	return out, rows.Err()
}

// CreateRoom provisions a bookable resource mailbox: a directory user with
// display_type DT_ROOM (or DT_EQUIPMENT when equipment is set), no password (a
// resource cannot sign in), its PR_DISPLAY_NAME, and - when positive - its seating
// capacity, in one transaction. maildir is the resource's object-store path, which
// the caller derives the same way it does for a user. It reports the new user id.
func (d *SQLDirectory) CreateRoom(email, displayName, maildir string, capacity int, equipment bool) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return 0, errors.New("directory: room address must be an email address")
	}
	domain := email[at+1:]
	var domainID int64
	err := d.db.QueryRow(`SELECT id FROM domains WHERE domainname = ?`, domain).Scan(&domainID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("directory: domain %q not found", domain)
	}
	if err != nil {
		return 0, err
	}
	dt := dtRoom
	if equipment {
		dt = dtEquipment
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO users
		   (username, password, domain_id, homeserver, maildir, lang, timezone, privilege_bits, address_status, display_type)
		 VALUES (?, '', ?, 0, ?, '', '', 0, 0, ?)`,
		email, domainID, maildir, dt)
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
	if capacity > 0 {
		if _, err := tx.Exec(
			`INSERT INTO user_properties (user_id, proptag, order_id, propval_str) VALUES (?, ?, 1, ?)`,
			id, prRoomCapacity, strconv.Itoa(capacity)); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
