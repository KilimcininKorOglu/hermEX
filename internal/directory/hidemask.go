package directory

import (
	"database/sql"
	"log"
)

// syncHideMask recomputes one user's users.hide_mask from its PR_ATTR_HIDDEN
// properties inside tx. It locks the user row first, the same order every writer
// of the column takes, so a start-up recompute and a concurrent toggle serialize
// instead of one overwriting the other with a stale value.
func syncHideMask(tx *sql.Tx, userID int64) error {
	var locked int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = ? FOR UPDATE`, userID).Scan(&locked); err != nil {
		return err
	}
	rows, err := tx.Query(
		`SELECT proptag, propval_str FROM user_properties WHERE user_id = ? AND order_id = 1 AND proptag IN (?, ?)`,
		userID, PropAttrHiddenMask, PropAttrHidden)
	if err != nil {
		return err
	}
	props := map[uint32]string{}
	for rows.Next() {
		var tag uint32
		var val sql.NullString
		if err := rows.Scan(&tag, &val); err != nil {
			rows.Close()
			return err
		}
		props[tag] = val.String
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE users SET hide_mask = ? WHERE id = ?`, HideMask(props), userID)
	return err
}

// syncHideMasks recomputes users.hide_mask for every user that has a hide
// property or a non-zero column, the users whose column can be out of step: a
// store just migrated, a restored dump, or a property an older binary wrote
// without the column. A failure is logged and leaves the column as it was; the
// GAL readers also apply the mask decoded from the properties, so a stale column
// can only withhold an entry, never show one an operator hid.
func (d *SQLDirectory) syncHideMasks() {
	ids, err := d.hideMaskCandidates()
	if err != nil {
		log.Printf("directory: could not list users to recompute address-book hide masks: %v", err)
		return
	}
	for _, id := range ids {
		if err := d.syncHideMaskOf(id); err != nil {
			log.Printf("directory: could not recompute the address-book hide mask of user %d: %v", id, err)
		}
	}
}

// hideMaskCandidates lists the users whose hide_mask may be out of step.
func (d *SQLDirectory) hideMaskCandidates() ([]int64, error) {
	rows, err := d.db.Query(
		`SELECT id FROM users WHERE hide_mask <> 0
		 UNION SELECT user_id FROM user_properties WHERE proptag IN (?, ?)`,
		PropAttrHiddenMask, PropAttrHidden)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// syncHideMaskOf recomputes one user's hide_mask in its own transaction.
func (d *SQLDirectory) syncHideMaskOf(id int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := syncHideMask(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// touchesHideMask reports whether a property write changes the hide mask.
func touchesHideMask(props map[uint32]string) bool {
	_, mask := props[PropAttrHiddenMask]
	_, boolean := props[PropAttrHidden]
	return mask || boolean
}
