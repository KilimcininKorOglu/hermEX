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

// roomQuery selects bookable resource mailboxes - rooms and equipment
// (display_type DT_ROOM/DT_EQUIPMENT) - that are enabled and have a mailbox,
// ordered by address, each with its display name and (when set) seating
// capacity. The same active-user/active-domain filter SearchGAL applies is used
// so a disabled room never appears. scope is either the caller's address-book
// scope predicate or empty for the unscoped operator view.
func roomQuery(scope string) string {
	return `
SELECT u.username, u.display_type, COALESCE(dn.propval_str, ''), COALESCE(cap.propval_str, '')
  FROM users u JOIN domains d ON u.domain_id = d.id
  LEFT JOIN user_properties dn ON dn.user_id = u.id AND dn.proptag = ? AND dn.order_id = 1
  LEFT JOIN user_properties cap ON cap.user_id = u.id AND cap.proptag = ? AND cap.order_id = 1
 WHERE u.display_type IN (?, ?)
   AND u.maildir <> ''
   AND (u.address_status & ?) = ?
   AND (u.address_status & ?) = 0
   AND d.domain_status = 0` + scope + `
 ORDER BY u.username`
}

// ListRooms returns the bookable resource mailboxes visible to caller (see
// galScopePredicate), for the webmail room picker and the EWS room finder. A
// room is an address-book object like any other, so it is scoped the same way
// SearchGAL is: without that, an address the caller cannot find in the GAL would
// still be listed here.
func (d *SQLDirectory) ListRooms(caller string) ([]GALEntry, error) {
	scope, ok, err := d.galScope(caller)
	if err != nil || !ok {
		return nil, err
	}
	args := append([]any{prDisplayName, prRoomCapacity, dtRoom, dtEquipment, afUserMask, afUserNormal, afDomainMask}, scope...)
	return d.scanRooms(roomQuery(galScopePredicate), args...)
}

// ListAllRooms returns every bookable resource mailbox in the deployment,
// unscoped. It backs the operator surfaces in the admin panel, which manage
// resources across domains and are gated by the admin's own system/org/domain
// role. No end-user surface may call it.
func (d *SQLDirectory) ListAllRooms() ([]GALEntry, error) {
	return d.scanRooms(roomQuery(""),
		prDisplayName, prRoomCapacity, dtRoom, dtEquipment, afUserMask, afUserNormal, afDomainMask)
}

// scanRooms runs a room query and projects its rows.
func (d *SQLDirectory) scanRooms(q string, args ...any) (out []GALEntry, err error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var addr, name, capStr string
		var dt int
		if err := rows.Scan(&addr, &dt, &name, &capStr); err != nil {
			return nil, err
		}
		if name == "" {
			name = addr
		}
		capacity, err := parseRoomCapacity(capStr)
		if err != nil {
			return nil, fmt.Errorf("directory: room %s: %w", addr, err)
		}
		out = append(out, GALEntry{DisplayName: name, Address: addr, DisplayType: dt, Capacity: capacity})
	}
	return out, rows.Err()
}

// parseRoomCapacity reads a stored seating capacity. An empty value is "not
// given" (0); anything else must be a non-negative whole number. Every write path
// stores only such a value, so a row that is not one is corruption and is
// reported rather than shown as an unknown capacity.
func parseRoomCapacity(s string) (int, error) {
	if err := validateRoomCapacity(s); err != nil || s == "" {
		return 0, err
	}
	return strconv.Atoi(s)
}

// validateRoomCapacity reports whether s is a valid stored room capacity: empty
// ("not given") or a non-negative whole number.
func validateRoomCapacity(s string) error {
	if s == "" {
		return nil
	}
	if n, err := strconv.Atoi(s); err != nil || n < 0 {
		return fmt.Errorf("room capacity %q is not a non-negative whole number", s)
	}
	return nil
}

// CreateRoom provisions a bookable resource mailbox: a directory user with
// display_type DT_ROOM (or DT_EQUIPMENT when equipment is set), no password (a
// resource cannot sign in), its PR_DISPLAY_NAME, and - when positive - its seating
// capacity, in one transaction. maildir is the resource's object-store path, which
// the caller derives the same way it does for a user. It reports the new user id.
func (d *SQLDirectory) CreateRoom(email, displayName, maildir string, capacity int, equipment bool) (id int64, err error) {
	// 0 means the capacity was not given; a negative one is an error, not a silent
	// "not given".
	if capacity < 0 {
		return 0, fmt.Errorf("directory: room capacity %d is negative", capacity)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	domainID, err := d.resourceDomainID(email)
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
	defer rollbackTx(tx, &err)
	if err := checkAddressUnclaimed(tx, email); err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		`INSERT INTO users
		   (username, password, domain_id, homeserver, maildir, lang, timezone, privilege_bits, address_status, display_type)
		 VALUES (?, '', ?, 0, ?, '', '', 0, 0, ?)`,
		email, domainID, maildir, dt)
	if err != nil {
		return 0, err
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertRoomProperties(tx, id, displayName, capacity); err != nil {
		return 0, err
	}
	if err := provisionMailbox(maildir); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// resourceDomainID validates a resource address and resolves the domain it must
// be created in.
func (d *SQLDirectory) resourceDomainID(email string) (int64, error) {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return 0, errors.New("directory: room address must be an email address")
	}
	if err := ValidateAddress(email); err != nil {
		return 0, err
	}
	domain := email[at+1:]
	var domainID int64
	err := d.db.QueryRow(`SELECT id FROM domains WHERE domainname = ?`, domain).Scan(&domainID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("directory: domain %q not found", domain)
	}
	return domainID, err
}

// insertRoomProperties stamps the resource's display name and, when positive,
// its seating capacity. An unset field writes no row at all.
func insertRoomProperties(tx *sql.Tx, id int64, displayName string, capacity int) error {
	const insert = `INSERT INTO user_properties (user_id, proptag, order_id, propval_str) VALUES (?, ?, 1, ?)`
	if name := strings.TrimSpace(displayName); name != "" {
		if _, err := tx.Exec(insert, id, prDisplayName, name); err != nil {
			return err
		}
	}
	if capacity > 0 {
		if _, err := tx.Exec(insert, id, prRoomCapacity, strconv.Itoa(capacity)); err != nil {
			return err
		}
	}
	return nil
}
