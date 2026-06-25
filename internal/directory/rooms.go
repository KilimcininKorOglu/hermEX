package directory

// ListRooms returns the organization's bookable resource mailboxes - rooms and
// equipment (display_type DT_ROOM/DT_EQUIPMENT) - for the webmail room picker:
// enabled entries with a mailbox, ordered by address. Capacity is not modeled, so
// only the address and display name are returned (as GALEntry, reusing the GAL row
// shape). The same active-user/active-domain filter SearchGAL applies is used so a
// disabled room never appears.
func (d *SQLDirectory) ListRooms() ([]GALEntry, error) {
	const prDisplayName = 0x3001001F // PR_DISPLAY_NAME (PtUnicode)
	const q = `
SELECT u.username, u.display_type, COALESCE(dn.propval_str, '')
  FROM users u JOIN domains d ON u.domain_id = d.id
  LEFT JOIN user_properties dn ON dn.user_id = u.id AND dn.proptag = ? AND dn.order_id = 1
 WHERE u.display_type IN (?, ?)
   AND u.maildir <> ''
   AND (u.address_status & ?) = ?
   AND (u.address_status & ?) = 0
   AND d.domain_status = 0
 ORDER BY u.username`
	rows, err := d.db.Query(q, prDisplayName, dtRoom, dtEquipment, afUserMask, afUserNormal, afDomainMask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GALEntry
	for rows.Next() {
		var addr, name string
		var dt int
		if err := rows.Scan(&addr, &dt, &name); err != nil {
			return nil, err
		}
		if name == "" {
			name = addr
		}
		out = append(out, GALEntry{DisplayName: name, Address: addr, DisplayType: dt})
	}
	return out, rows.Err()
}
