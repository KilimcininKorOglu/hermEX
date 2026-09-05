package objectstore

import "database/sql"

// BackfillPreviews fills the message-list projections on index rows written
// before the columns existed. It returns how many rows it updated.
//
// A migration cannot compute these: the snippet comes from the body and the
// attachment flag from the attachment rows, which is object-store state SQL
// cannot read. So the columns default to the empty answer and this walks the
// rows that still hold it. A row whose message no longer opens is left alone
// rather than failing the run, because one broken message must not stop the rest
// of the mailbox from being repaired.
func (s *Store) BackfillPreviews() (int, error) {
	ids, err := s.rowsMissingProjections()
	if err != nil {
		return 0, err
	}

	var updated int
	for _, id := range ids {
		msg, err := s.OpenMessage(id)
		if err != nil {
			s.logStoreError("backfill-previews.open", err)
			continue
		}
		preview := projectPreview(msg.Props)
		hasAttach := projectHasAttachments(msg)
		if preview == "" && !hasAttach {
			// Nothing to record. Leaving the row as it is keeps a later run from
			// reporting work it did not do.
			continue
		}
		if err := s.setListProjections(id, preview, hasAttach); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// rowsMissingProjections lists the index rows that still hold the empty answer
// both columns default to.
func (s *Store) rowsMissingProjections() ([]int64, error) {
	rows, err := s.idxdb.Query(
		`SELECT message_id FROM messages WHERE preview = '' AND has_attach = 0`)
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

// setListProjections writes one row's message-list projections.
func (s *Store) setListProjections(messageID int64, preview string, hasAttach bool) error {
	_, err := s.idxdb.Exec(
		`UPDATE messages SET preview=?, has_attach=? WHERE message_id=?`,
		preview, boolToInt(hasAttach), messageID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	return nil
}
