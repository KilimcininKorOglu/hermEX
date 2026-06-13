package objectstore

import (
	"database/sql"
	"errors"

	"hermex/internal/oxcmail"
)

// OpenMessage reconstructs a stored message as a MAPI message object: its
// top-level property bag, its recipient bags (in recipient_id order), and its
// attachment bags (in attachment_id order), with content properties reloaded
// from their content files. It is the read primitive behind wire-form
// re-synthesis (GetMessageRaw) and the object view, and reports ErrNotFound
// when no such message exists.
func (s *Store) OpenMessage(messageID int64) (*oxcmail.Message, error) {
	var exists int
	err := s.objdb.QueryRow(`SELECT 1 FROM messages WHERE message_id=?`, messageID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	props, err := s.GetMessageProperties(messageID)
	if err != nil {
		return nil, err
	}
	msg := &oxcmail.Message{Props: props}

	rids, err := s.objChildIDs(`SELECT recipient_id FROM recipients WHERE message_id=? ORDER BY recipient_id`, messageID)
	if err != nil {
		return nil, err
	}
	for _, rid := range rids {
		rprops, err := s.GetRecipientProperties(rid)
		if err != nil {
			return nil, err
		}
		msg.Recipients = append(msg.Recipients, rprops)
	}

	aids, err := s.objChildIDs(`SELECT attachment_id FROM attachments WHERE message_id=? ORDER BY attachment_id`, messageID)
	if err != nil {
		return nil, err
	}
	for _, aid := range aids {
		aprops, err := s.GetAttachmentProperties(aid)
		if err != nil {
			return nil, err
		}
		msg.Attachments = append(msg.Attachments, oxcmail.Attachment{Props: aprops})
	}

	return msg, nil
}

// objChildIDs returns the ids of a message's recipient or attachment rows, in
// ascending id order.
func (s *Store) objChildIDs(query string, messageID int64) ([]int64, error) {
	rows, err := s.objdb.Query(query, messageID)
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
