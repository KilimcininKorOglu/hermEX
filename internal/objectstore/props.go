package objectstore

import (
	"database/sql"
	"fmt"
	"strings"

	"hermex/internal/mapi"
)

// SetStoreProperties upserts properties on the object store root (the
// store_properties table, keyed by proptag alone).
func (s *Store) SetStoreProperties(props mapi.PropertyValues) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO store_properties (proptag, propval) VALUES (?, ?)
		 ON CONFLICT(proptag) DO UPDATE SET propval = excluded.propval`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range props {
		enc, err := encodeValue(p.Tag.Type(), p.Value)
		if err != nil {
			return fmt.Errorf("objectstore: encode %s: %w", p.Tag, err)
		}
		if _, err := stmt.Exec(int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetStoreProperties returns the requested store-root properties; with no tags
// it returns all of them.
func (s *Store) GetStoreProperties(tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	query := "SELECT proptag, propval FROM store_properties"
	var args []any
	if len(tags) > 0 {
		ph := make([]string, len(tags))
		for i, t := range tags {
			ph[i] = "?"
			args = append(args, int64(uint32(t)))
		}
		query += " WHERE proptag IN (" + strings.Join(ph, ",") + ")"
	}
	return scanProps(s.objdb, query, args)
}

// SetFolderProperties upserts properties on a folder.
func (s *Store) SetFolderProperties(folderID int64, props mapi.PropertyValues) error {
	return s.setObjectProps("folder_properties", "folder_id", folderID, props)
}

// GetFolderProperties returns the requested folder properties; with no tags it
// returns all of them.
func (s *Store) GetFolderProperties(folderID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	return s.getObjectProps("folder_properties", "folder_id", folderID, tags)
}

// SetMessageProperties upserts properties on a message.
func (s *Store) SetMessageProperties(messageID int64, props mapi.PropertyValues) error {
	return s.setObjectProps("message_properties", "message_id", messageID, props)
}

// GetMessageProperties returns the requested message properties; with no tags
// it returns all of them.
func (s *Store) GetMessageProperties(messageID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	return s.getObjectProps("message_properties", "message_id", messageID, tags)
}

// SetRecipientProperties upserts properties on a recipient.
func (s *Store) SetRecipientProperties(recipientID int64, props mapi.PropertyValues) error {
	return s.setObjectProps("recipients_properties", "recipient_id", recipientID, props)
}

// GetRecipientProperties returns the requested recipient properties; with no
// tags it returns all of them.
func (s *Store) GetRecipientProperties(recipientID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	return s.getObjectProps("recipients_properties", "recipient_id", recipientID, tags)
}

// SetAttachmentProperties upserts properties on an attachment.
func (s *Store) SetAttachmentProperties(attachmentID int64, props mapi.PropertyValues) error {
	return s.setObjectProps("attachment_properties", "attachment_id", attachmentID, props)
}

// GetAttachmentProperties returns the requested attachment properties; with no
// tags it returns all of them.
func (s *Store) GetAttachmentProperties(attachmentID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	return s.getObjectProps("attachment_properties", "attachment_id", attachmentID, tags)
}

// setObjectProps upserts a property bag into an object's _properties table.
// table and idCol are internal constants, never caller input, so interpolating
// them into the SQL is safe.
func (s *Store) setObjectProps(table, idCol string, id int64, props mapi.PropertyValues) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(fmt.Sprintf(
		`INSERT INTO %s (%s, proptag, propval) VALUES (?, ?, ?)
		 ON CONFLICT(%s, proptag) DO UPDATE SET propval = excluded.propval`,
		table, idCol, idCol))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range props {
		enc, err := encodeValue(p.Tag.Type(), p.Value)
		if err != nil {
			return fmt.Errorf("objectstore: encode %s: %w", p.Tag, err)
		}
		if _, err := stmt.Exec(id, int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) getObjectProps(table, idCol string, id int64, tags []mapi.PropTag) (mapi.PropertyValues, error) {
	query := fmt.Sprintf("SELECT proptag, propval FROM %s WHERE %s = ?", table, idCol)
	args := []any{id}
	if len(tags) > 0 {
		ph := make([]string, len(tags))
		for i, t := range tags {
			ph[i] = "?"
			args = append(args, int64(uint32(t)))
		}
		query += " AND proptag IN (" + strings.Join(ph, ",") + ")"
	}
	return scanProps(s.objdb, query, args)
}

// scanProps runs a (proptag, propval) query and decodes each row.
func scanProps(db *sql.DB, query string, args []any) (mapi.PropertyValues, error) {
	rows, err := db.Query(query, args...)
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
		val, err := decodeValue(tag.Type(), col)
		if err != nil {
			return nil, fmt.Errorf("objectstore: decode %s: %w", tag, err)
		}
		out = append(out, mapi.TaggedPropVal{Tag: tag, Value: val})
	}
	return out, rows.Err()
}
