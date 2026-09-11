package objectstore

import (
	"fmt"
	"strings"

	"hermex/internal/mapi"
)

// maxBatchParams bounds the host parameters one batched property query binds.
// SQLite's default limit is 999 and it counts the ids and the proptags together,
// so the id chunk shrinks as the caller asks for more tags.
const maxBatchParams = 900

// batchIDChunk reports how many ids one query may carry alongside tagCount tags.
// It never returns zero, so a caller with a very long tag list still makes
// progress one id at a time rather than looping forever on an empty chunk.
func batchIDChunk(tagCount int) int {
	return max(maxBatchParams-tagCount, 1)
}

// MessagePropertiesBatch reads the given properties for many messages, one query
// per chunk of ids instead of one query per message. It exists because a caller
// that filters or sorts a whole folder reads the same few properties for every
// row, and a query per row makes that cost one round trip per message.
//
// A message with none of the requested properties is absent from the result
// rather than present and empty, exactly as GetMessageProperties returns an empty
// bag for it.
//
// An empty tag list returns an empty result. The guard below saves the round trip
// and nothing else: SQLite accepts `proptag IN ()` and answers it with no rows, so
// the result is the same either way.
func (s *Store) MessagePropertiesBatch(ids []int64, tags []mapi.PropTag) (map[int64]mapi.PropertyValues, error) {
	out := make(map[int64]mapi.PropertyValues, len(ids))
	if len(ids) == 0 || len(tags) == 0 {
		return out, nil
	}
	chunk := batchIDChunk(len(tags))
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		if err := s.propertiesChunk(ids[start:end], tags, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// propertiesChunk reads one bounded batch of ids into out.
func (s *Store) propertiesChunk(ids []int64, tags []mapi.PropTag, out map[int64]mapi.PropertyValues) error {
	args := make([]any, 0, len(ids)+len(tags))
	idPH := make([]string, len(ids))
	for i, id := range ids {
		idPH[i] = "?"
		args = append(args, id)
	}
	tagPH := make([]string, len(tags))
	for i, t := range tags {
		tagPH[i] = "?"
		args = append(args, int64(uint32(t)))
	}
	// #nosec G201 -- the formatted parts are runs of ? placeholders; every value travels as a query argument
	query := fmt.Sprintf(
		`SELECT message_id, proptag, propval FROM message_properties
		 WHERE message_id IN (%s) AND proptag IN (%s)`,
		strings.Join(idPH, ","), strings.Join(tagPH, ","))
	rows, err := s.objdb.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mid, rawTag int64
		var col any
		if err := rows.Scan(&mid, &rawTag, &col); err != nil {
			return err
		}
		// #nosec G115 -- a proptag is 32 bits and was stored as one
		tag := mapi.PropTag(uint32(rawTag))
		val, err := s.loadPropval(tag, col)
		if err != nil {
			return fmt.Errorf("objectstore: decode %s: %w", tag, err)
		}
		bag := out[mid]
		bag.Set(tag, val)
		out[mid] = bag
	}
	return rows.Err()
}
