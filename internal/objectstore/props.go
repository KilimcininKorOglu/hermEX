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
		enc, err := s.storedPropval(p.Tag, p.Value)
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
	return s.scanProps(query, args)
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

// setObjectProps upserts a property bag into an object's _properties table in
// its own transaction.
func (s *Store) setObjectProps(table, idCol string, id int64, props mapi.PropertyValues) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insertProps(tx, table, idCol, id, props); err != nil {
		return err
	}
	return tx.Commit()
}

// insertProps upserts a property bag into an object's (idCol, proptag) property
// table within the caller's transaction, so an object's properties commit
// atomically with the object row that owns them. Content properties are
// offloaded to content files (see storedPropval). table and idCol are internal
// constants, never caller input, so interpolating them into the SQL is safe.
func (s *Store) insertProps(tx *sql.Tx, table, idCol string, id int64, props mapi.PropertyValues) error {
	stmt, err := tx.Prepare(fmt.Sprintf(
		`INSERT INTO %s (%s, proptag, propval) VALUES (?, ?, ?)
		 ON CONFLICT(%s, proptag) DO UPDATE SET propval = excluded.propval`,
		table, idCol, idCol))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range props {
		enc, err := s.storedPropval(p.Tag, p.Value)
		if err != nil {
			return fmt.Errorf("objectstore: encode %s: %w", p.Tag, err)
		}
		if _, err := stmt.Exec(id, int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return nil
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
	return s.scanProps(query, args)
}

// scanProps runs a (proptag, propval) query against the object store and decodes
// each row, reversing the content-offload (see loadPropval) for content
// properties.
func (s *Store) scanProps(query string, args []any) (mapi.PropertyValues, error) {
	rows, err := s.objdb.Query(query, args...)
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
		val, err := s.loadPropval(tag, col)
		if err != nil {
			return nil, fmt.Errorf("objectstore: decode %s: %w", tag, err)
		}
		out = append(out, mapi.TaggedPropVal{Tag: tag, Value: val})
	}
	return out, rows.Err()
}

// storedPropval computes the value written into a propval column. Content
// properties (bodies, attachment payloads — see isCIDProp) are offloaded to a
// content file and the column holds the returned content id; every other
// property is encoded inline as a queryable scalar or a length-prefixed blob.
func (s *Store) storedPropval(tag mapi.PropTag, v any) (any, error) {
	if isCIDProp(tag) {
		raw, err := contentBytes(tag.Type(), v)
		if err != nil {
			return nil, err
		}
		return s.putContent(raw)
	}
	return encodeValue(tag.Type(), v)
}

// loadPropval reverses storedPropval for a scanned column: a content property's
// column holds a content id whose file is read back and reassembled into the
// property value; every other column decodes inline.
func (s *Store) loadPropval(tag mapi.PropTag, col any) (any, error) {
	if isCIDProp(tag) {
		cid, err := asString(col)
		if err != nil {
			return nil, err
		}
		raw, err := s.getContent(cid)
		if err != nil {
			return nil, err
		}
		return contentValue(tag.Type(), raw)
	}
	return decodeValue(tag.Type(), col)
}

// contentBytes extracts the raw payload bytes of an offloaded property: binary
// values pass through, string values are taken as their UTF-8 bytes.
func contentBytes(typ mapi.PropType, v any) ([]byte, error) {
	switch typ {
	case mapi.PtBinary:
		return asType[[]byte](v)
	case mapi.PtString8, mapi.PtUnicode:
		str, err := asType[string](v)
		return []byte(str), err
	default:
		return nil, fmt.Errorf("objectstore: cannot offload property type %#x", typ)
	}
}

// contentValue reassembles an offloaded property value from its raw payload
// bytes, inverting contentBytes.
func contentValue(typ mapi.PropType, raw []byte) (any, error) {
	switch typ {
	case mapi.PtBinary:
		return raw, nil
	case mapi.PtString8, mapi.PtUnicode:
		return string(raw), nil
	default:
		return nil, fmt.Errorf("objectstore: cannot reload property type %#x", typ)
	}
}

// asString coerces a scanned text/blob column to a string. modernc returns BLOB
// columns as []byte even when they hold text, so both forms are tolerated.
func asString(col any) (string, error) {
	switch c := col.(type) {
	case string:
		return c, nil
	case []byte:
		return string(c), nil
	default:
		return "", fmt.Errorf("objectstore: content id column is %T", col)
	}
}
