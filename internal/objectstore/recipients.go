package objectstore

import (
	"database/sql"
	"errors"
	"strings"

	"hermex/internal/mapi"
)

// ReplaceRecipients replaces a message's recipient set in place, keeping the
// message id, in one transaction that also allocates a fresh change number. The
// rows are written in the order given. A new recipient whose SMTP address
// (compared without case) matches a current one keeps every property of the old
// row that its new bag does not set, so what a client learned about an attendee,
// such as the response status an iTIP reply stored, survives an edit that does not
// know about it. A current recipient absent from the new set is removed. It
// reports ErrNotFound when no such message exists.
func (s *Store) ReplaceRecipients(messageID int64, recips []mapi.PropertyValues) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireMessage(tx, messageID); err != nil {
		return err
	}
	if err := s.rewriteRecipients(tx, messageID, recips); err != nil {
		return err
	}
	cn, err := allocateCN(tx)
	if err != nil {
		return err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	if _, err := tx.Exec(`UPDATE messages SET change_number=? WHERE message_id=?`, int64(cn), messageID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.refreshEML(messageID)
	s.publishChange("modify", cn, "")
	return nil
}

// rewriteRecipients writes the new recipient rows, carrying what each matched old
// row knew, then removes every old row.
func (s *Store) rewriteRecipients(tx *sql.Tx, messageID int64, recips []mapi.PropertyValues) error {
	oldIDs, byAddr, err := s.recipientsByAddress(tx, messageID)
	if err != nil {
		return err
	}
	for _, bag := range recips {
		if err := s.insertCarriedRecipient(tx, messageID, bag, byAddr); err != nil {
			return err
		}
	}
	for _, id := range oldIDs {
		if _, err := tx.Exec(`DELETE FROM recipients WHERE recipient_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// requireMessage reports ErrNotFound when no message has the id.
func requireMessage(tx *sql.Tx, messageID int64) error {
	var exists int
	err := tx.QueryRow(`SELECT 1 FROM messages WHERE message_id=?`, messageID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// recipientsByAddress returns a message's recipient row ids in row order and the
// first row holding each SMTP address, keyed in lower case.
func (s *Store) recipientsByAddress(tx *sql.Tx, messageID int64) ([]int64, map[string]int64, error) {
	rows, err := tx.Query(
		`SELECT r.recipient_id, p.proptag, p.propval FROM recipients r
		 LEFT JOIN recipients_properties p ON p.recipient_id = r.recipient_id AND p.proptag IN (?, ?, ?)
		 WHERE r.message_id = ? ORDER BY r.recipient_id`,
		int64(uint32(mapi.PrSmtpAddress)), int64(uint32(mapi.PrEmailAddress)), int64(uint32(mapi.PrAddrType)), messageID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids, bags, err := s.scanAddressRows(rows)
	if err != nil {
		return nil, nil, err
	}
	byAddr := map[string]int64{}
	for _, id := range ids {
		addr := recipientAddress(bags[id])
		if _, dup := byAddr[addr]; addr != "" && !dup {
			byAddr[addr] = id
		}
	}
	return ids, byAddr, nil
}

// scanAddressRows collects the (recipient id, proptag, propval) rows of
// recipientsByAddress into each row's address bag, keeping the row order. A row
// with no address property arrives once with a NULL tag and gets an empty bag.
func (s *Store) scanAddressRows(rows *sql.Rows) ([]int64, map[int64]mapi.PropertyValues, error) {
	var ids []int64
	bags := map[int64]mapi.PropertyValues{}
	for rows.Next() {
		var id int64
		var tag sql.NullInt64
		var col any
		if err := rows.Scan(&id, &tag, &col); err != nil {
			return nil, nil, err
		}
		if _, seen := bags[id]; !seen {
			ids = append(ids, id)
			bags[id] = nil
		}
		if !tag.Valid {
			continue
		}
		// #nosec G115 -- a proptag is 32 bits and was stored as one
		pt := mapi.PropTag(uint32(tag.Int64))
		v, err := s.loadPropval(pt, col)
		if err != nil {
			return nil, nil, err
		}
		bags[id] = append(bags[id], mapi.TaggedPropVal{Tag: pt, Value: v})
	}
	return ids, bags, rows.Err()
}

// insertCarriedRecipient writes one recipient row from bag. When its address
// matches a current row still unclaimed, the old row's properties the bag does not
// set are copied onto the new row as stored, and the old row is claimed so a
// second recipient with the same address starts clean.
func (s *Store) insertCarriedRecipient(tx *sql.Tx, messageID int64, bag mapi.PropertyValues, byAddr map[string]int64) error {
	res, err := tx.Exec(`INSERT INTO recipients (message_id) VALUES (?)`, messageID)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := s.insertProps(tx, "recipients_properties", "recipient_id", id, bag); err != nil {
		return err
	}
	addr := recipientAddress(bag)
	oldID, ok := byAddr[addr]
	if addr == "" || !ok {
		return nil
	}
	delete(byAddr, addr)
	_, err = tx.Exec(
		`INSERT INTO recipients_properties (recipient_id, proptag, propval)
		 SELECT ?, proptag, propval FROM recipients_properties WHERE recipient_id = ?
		 ON CONFLICT(recipient_id, proptag) DO NOTHING`, id, oldID)
	return err
}

// recipientAddress returns a recipient's SMTP address in lower case: its
// PrSmtpAddress, else its PrEmailAddress when the address type is SMTP.
func recipientAddress(bag mapi.PropertyValues) string {
	if addr, _ := stringProp(bag, mapi.PrSmtpAddress); strings.TrimSpace(addr) != "" {
		return strings.ToLower(strings.TrimSpace(addr))
	}
	if typ, _ := stringProp(bag, mapi.PrAddrType); strings.EqualFold(strings.TrimSpace(typ), "SMTP") {
		addr, _ := stringProp(bag, mapi.PrEmailAddress)
		return strings.ToLower(strings.TrimSpace(addr))
	}
	return ""
}
