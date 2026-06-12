package store

import (
	"fmt"
	"strings"

	"hermex/internal/mapi"
)

// SetFolderProperties inserts or replaces properties on a folder.
func (s *Store) SetFolderProperties(folderID int64, props mapi.PropertyValues) error {
	return s.setProperties("folder_properties", "folder_id", folderID, props)
}

// GetFolderProperties returns the requested folder properties; with no tags it
// returns all of them.
func (s *Store) GetFolderProperties(folderID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	return s.getProperties("folder_properties", "folder_id", folderID, tags)
}

// SetMessageProperties inserts or replaces properties on a message.
func (s *Store) SetMessageProperties(messageID int64, props mapi.PropertyValues) error {
	return s.setProperties("message_properties", "message_id", messageID, props)
}

// GetMessageProperties returns the requested message properties; with no tags
// it returns all of them.
func (s *Store) GetMessageProperties(messageID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	return s.getProperties("message_properties", "message_id", messageID, tags)
}

// setProperties upserts a property bag. table and idCol are internal constants,
// never caller input, so interpolating them into the SQL is safe.
func (s *Store) setProperties(table, idCol string, id int64, props mapi.PropertyValues) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(fmt.Sprintf(
		`INSERT INTO %s (%s, proptag, value) VALUES (?, ?, ?)
		 ON CONFLICT(%s, proptag) DO UPDATE SET value = excluded.value`,
		table, idCol, idCol))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range props {
		enc, err := encodeStoredValue(p.Tag.Type(), p.Value)
		if err != nil {
			return fmt.Errorf("store: encode %s: %w", p.Tag, err)
		}
		if _, err := stmt.Exec(id, int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) getProperties(table, idCol string, id int64, tags []mapi.PropTag) (mapi.PropertyValues, error) {
	query := fmt.Sprintf("SELECT proptag, value FROM %s WHERE %s = ?", table, idCol)
	args := []any{id}
	if len(tags) > 0 {
		placeholders := make([]string, len(tags))
		for i, t := range tags {
			placeholders[i] = "?"
			args = append(args, int64(uint32(t)))
		}
		query += " AND proptag IN (" + strings.Join(placeholders, ",") + ")"
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out mapi.PropertyValues
	for rows.Next() {
		var rawTag int64
		var col any
		if err := rows.Scan(&rawTag, &col); err != nil {
			return nil, err
		}
		tag := mapi.PropTag(uint32(rawTag))
		val, err := decodeStoredValue(tag.Type(), col)
		if err != nil {
			return nil, fmt.Errorf("store: decode %s: %w", tag, err)
		}
		out = append(out, mapi.TaggedPropVal{Tag: tag, Value: val})
	}
	return out, rows.Err()
}
