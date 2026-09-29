package objectstore

import (
	"encoding/json"
	"slices"

	"hermex/internal/mapi"
)

// The distinct-tag queries take their owner ids as one JSON array parameter, so
// any number of ids binds as a single value.
const (
	messagePropTagsQuery = `SELECT DISTINCT proptag FROM message_properties WHERE message_id IN (SELECT value FROM json_each(?))`
	folderPropTagsQuery  = `SELECT DISTINCT proptag FROM folder_properties WHERE folder_id IN (SELECT value FROM json_each(?))`
)

// MessagePropTags returns every property tag stored on at least one of the given
// messages, once each and in ascending order: the columns a contents table of
// those messages can produce from storage ([MS-OXCTABL] 2.2.2.3).
func (s *Store) MessagePropTags(ids []int64) ([]mapi.PropTag, error) {
	return s.storedPropTags(messagePropTagsQuery, ids)
}

// FolderPropTags returns every property tag stored on at least one of the given
// folders, once each and in ascending order.
func (s *Store) FolderPropTags(ids []int64) ([]mapi.PropTag, error) {
	return s.storedPropTags(folderPropTagsQuery, ids)
}

// storedPropTags runs one of the distinct-tag queries over ids.
func (s *Store) storedPropTags(query string, ids []int64) ([]mapi.PropTag, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	arg, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	tags, err := s.queryTags(query, string(arg))
	if err != nil {
		s.logStoreError("stored-proptags", err)
		return nil, err
	}
	slices.Sort(tags)
	return tags, nil
}

// queryTags runs a query whose one column is a stored proptag.
func (s *Store) queryTags(query string, args ...any) ([]mapi.PropTag, error) {
	rows, err := s.objdb.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mapi.PropTag
	for rows.Next() {
		var raw int64
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		// #nosec G115 -- a proptag is 32 bits and was stored as one
		out = append(out, mapi.PropTag(uint32(raw)))
	}
	return out, rows.Err()
}
