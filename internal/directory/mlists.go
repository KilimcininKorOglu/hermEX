package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// list_type (mlists.list_type): how a distribution list's membership is computed.
// Only normal and domain are live; the reference's group/dyngroup types are
// defunct.
const (
	mlistTypeNormal = 0 // the explicit members in associations
	mlistTypeDomain = 2 // every mailbox user in the list's domain
)

// list_privilege (mlists.list_privilege): who may post to a distribution list.
const (
	mlistPrivAll       = 0 // anyone
	mlistPrivInternal  = 1 // only the list's own members
	mlistPrivDomain    = 2 // only senders in the list's domain
	mlistPrivSpecified = 3 // only senders named in specifieds
	mlistPrivOutgoing  = 4 // anyone (an announce-only list)
)

// MListResult is the outcome of expanding a distribution-list address.
type MListResult int

const (
	MListOK              MListResult = iota // expanded; members returned
	MListNone                               // the address is not a distribution list
	MListPrivilDomain                       // the sender's domain may not post to the list
	MListPrivilInternal                     // the sender is not an internal member
	MListPrivilSpecified                    // the sender is not a permitted specified sender
)

// MListInfo is a distribution list's administrative summary.
type MListInfo struct {
	ID       int64
	Listname string
	ListType int
	ListPriv int
}

// addrDomain returns the domain part of an address, or "" when there is no '@'.
func addrDomain(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}

// ExpandMList resolves a distribution-list address to its direct members,
// applying the list's posting privilege against the sender (from). It mirrors the
// the reference list-member lookup: one level only — a member that is itself a list is
// returned verbatim, and the caller recurses under its own loop guard. The
// address-book uses from == listAddr to bypass the privilege gate; the MTA passes
// the real sender. A non-OK result means no members are returned: MListNone says
// "not a list, resolve it normally", the PRIVIL_* codes are posting refusals.
func (d *SQLDirectory) ExpandMList(listAddr, from string) ([]string, MListResult, error) {
	listAddr = strings.ToLower(strings.TrimSpace(listAddr))
	from = strings.ToLower(strings.TrimSpace(from))
	listDomain := addrDomain(listAddr)
	fromDomain := addrDomain(from)
	if listDomain == "" || fromDomain == "" {
		return nil, MListNone, nil
	}

	var id int64
	var listType, listPriv int
	err := d.db.QueryRow(
		`SELECT id, list_type, list_privilege FROM mlists WHERE listname = ?`, listAddr).
		Scan(&id, &listType, &listPriv)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, MListNone, nil
	}
	if err != nil {
		return nil, MListNone, err
	}

	// Posting privilege. chkIntl defers the "sender must be a member" test to the
	// expansion, where the member set is already loaded.
	chkIntl := false
	switch listPriv {
	case mlistPrivAll, mlistPrivOutgoing:
	case mlistPrivInternal:
		chkIntl = true
	case mlistPrivDomain:
		if !strings.EqualFold(listDomain, fromDomain) {
			return nil, MListPrivilDomain, nil
		}
	case mlistPrivSpecified:
		ok, err := d.senderIsSpecified(id, from, fromDomain)
		if err != nil {
			return nil, MListNone, err
		}
		if !ok {
			return nil, MListPrivilSpecified, nil
		}
	default:
		return nil, MListNone, nil
	}

	var members []string
	switch listType {
	case mlistTypeNormal:
		members, err = d.listAssociations(id)
	case mlistTypeDomain:
		members, err = d.domainMailusers(listDomain)
	default:
		return nil, MListNone, nil
	}
	if err != nil {
		return nil, MListNone, err
	}
	if chkIntl && !slices.ContainsFunc(members, func(m string) bool { return strings.EqualFold(m, from) }) {
		return nil, MListPrivilInternal, nil
	}
	return members, MListOK, nil
}

// senderIsSpecified reports whether from (or its domain) is named in the list's
// specifieds set.
func (d *SQLDirectory) senderIsSpecified(listID int64, from, fromDomain string) (bool, error) {
	rows, err := d.db.Query(`SELECT username FROM specifieds WHERE list_id = ?`, listID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return false, err
		}
		if strings.EqualFold(s, from) || strings.EqualFold(s, fromDomain) {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// listAssociations returns the explicit member addresses of a normal-type list.
func (d *SQLDirectory) listAssociations(listID int64) ([]string, error) {
	rows, err := d.db.Query(`SELECT username FROM associations WHERE list_id = ?`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// domainMailusers returns every mailbox user (display_type DT_MAILUSER) in a
// domain — the membership of a domain-type list.
func (d *SQLDirectory) domainMailusers(domain string) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT u.username FROM users u JOIN domains d ON u.domain_id = d.id
		  WHERE d.domainname = ? AND u.display_type = ?`,
		strings.ToLower(domain), dtMailuser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateMList creates a distribution list: a users row (display_type DT_DISTLIST,
// no password or maildir, so it cannot log in) plus its mlists policy row, in one
// transaction. The list's domain must already exist; the address must be unused.
func (d *SQLDirectory) CreateMList(listname string, listType, listPriv int) (int64, error) {
	listname = strings.ToLower(strings.TrimSpace(listname))
	at := strings.LastIndexByte(listname, '@')
	if at <= 0 {
		return 0, errors.New("directory: list name must be an email address")
	}
	domain := listname[at+1:]
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
	if _, err := tx.Exec(
		`INSERT INTO users (username, domain_id, display_type, password, maildir) VALUES (?, ?, ?, '', '')`,
		listname, domainID, dtDistlist); err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		`INSERT INTO mlists (listname, domain_id, list_type, list_privilege) VALUES (?, ?, ?, ?)`,
		listname, domainID, listType, listPriv)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DeleteMList removes a distribution list. Deleting its users row cascades to the
// mlists, associations and specifieds rows; aliases (which have no foreign key)
// are dropped explicitly. It reports whether a list was removed.
func (d *SQLDirectory) DeleteMList(listname string) (bool, error) {
	listname = strings.ToLower(strings.TrimSpace(listname))
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM users WHERE username = ? AND display_type = ?`, listname, dtDistlist)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM aliases WHERE mainname = ?`, listname); err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, tx.Commit()
}

// ListMLists returns every distribution list, ordered by address, for the admin
// API.
func (d *SQLDirectory) ListMLists() ([]MListInfo, error) {
	rows, err := d.db.Query(
		`SELECT id, listname, list_type, list_privilege FROM mlists ORDER BY listname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MListInfo
	for rows.Next() {
		var m MListInfo
		if err := rows.Scan(&m.ID, &m.Listname, &m.ListType, &m.ListPriv); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMembers replaces a normal-type list's explicit members with the given set
// (lowercased, trimmed, de-duplicated, blanks dropped), in one transaction,
// reporting whether the list existed.
func (d *SQLDirectory) SetMembers(listname string, members []string) (bool, error) {
	id, ok, err := d.mlistID(listname)
	if err != nil || !ok {
		return false, err
	}
	clean := cleanAddrs(members)
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM associations WHERE list_id = ?`, id); err != nil {
		return false, err
	}
	for _, m := range clean {
		if _, err := tx.Exec(`INSERT INTO associations (list_id, username) VALUES (?, ?)`, id, m); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// ListMembers returns a normal-type list's explicit members, ordered, for the
// admin detail view.
func (d *SQLDirectory) ListMembers(listname string) ([]string, error) {
	return d.listJoinByName("associations", listname)
}

// SetSpecifieds replaces a list's permitted-sender set (for the "specified"
// posting privilege) with the given addresses or bare domains.
func (d *SQLDirectory) SetSpecifieds(listname string, senders []string) (bool, error) {
	id, ok, err := d.mlistID(listname)
	if err != nil || !ok {
		return false, err
	}
	clean := cleanAddrs(senders)
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM specifieds WHERE list_id = ?`, id); err != nil {
		return false, err
	}
	for _, s := range clean {
		if _, err := tx.Exec(`INSERT INTO specifieds (list_id, username) VALUES (?, ?)`, id, s); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// ListSpecifieds returns a list's permitted senders, ordered, for the admin
// detail view.
func (d *SQLDirectory) ListSpecifieds(listname string) ([]string, error) {
	return d.listJoinByName("specifieds", listname)
}

// mlistID looks up a list's id by address, reporting whether it exists.
func (d *SQLDirectory) mlistID(listname string) (int64, bool, error) {
	var id int64
	err := d.db.QueryRow(`SELECT id FROM mlists WHERE listname = ?`,
		strings.ToLower(strings.TrimSpace(listname))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// listJoinByName returns the usernames in a list's membership table (associations
// or specifieds), ordered. The table name is a fixed internal literal, never
// client input.
func (d *SQLDirectory) listJoinByName(table, listname string) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT t.username FROM `+table+` t JOIN mlists m ON t.list_id = m.id
		  WHERE m.listname = ? ORDER BY t.username`,
		strings.ToLower(strings.TrimSpace(listname)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// cleanAddrs lowercases, trims, de-duplicates and drops blanks from a list of
// addresses, preserving order.
func cleanAddrs(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, a := range in {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}
