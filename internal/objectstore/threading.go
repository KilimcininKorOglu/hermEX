package objectstore

import (
	"fmt"
	"strings"

	"hermex/internal/mapi"
)

// ThreadHeaders holds one message's RFC 5322 threading headers as stored by
// Import. References is the full space-separated ancestor chain (verbatim, not
// truncated), which RFC 5256 threading needs to link a reply across a parent
// that is not present in the folder.
type ThreadHeaders struct {
	MessageID  string // PR_INTERNET_MESSAGE_ID, e.g. "<abc@host>"
	References string // PR_INTERNET_REFERENCES, the space-separated chain
	InReplyTo  string // PR_IN_REPLY_TO_ID
}

// threadChunk bounds the message-id IN clause well under SQLite's host-parameter
// limit (leaving room for the three proptag parameters), so a very large folder
// is read in several queries rather than overflowing the bind limit.
const threadChunk = 900

// ConversationThreading batch-reads the RFC 5322 threading headers for the given
// message ids — a folder's messages, whose ids come from ListMessages — so a
// threaded list view does one query per chunk instead of a property read per
// message. The headers live in the message property bag (the IMAP index mirrors
// the reference schema and carries no message-id/references columns). Messages
// with none of the three headers are simply absent from the result map.
func (s *Store) ConversationThreading(messageIDs []int64) (map[int64]ThreadHeaders, error) {
	out := make(map[int64]ThreadHeaders, len(messageIDs))
	tags := []any{
		int64(uint32(mapi.PrInternetMessageID)),
		int64(uint32(mapi.PrInternetReferences)),
		int64(uint32(mapi.PrInReplyToID)),
	}
	for start := 0; start < len(messageIDs); start += threadChunk {
		end := min(start+threadChunk, len(messageIDs))
		chunk := messageIDs[start:end]
		if err := s.threadingChunk(chunk, tags, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// threadingChunk reads one bounded batch of message ids into out.
func (s *Store) threadingChunk(ids []int64, tags []any, out map[int64]ThreadHeaders) error {
	ph := make([]string, len(ids))
	args := make([]any, 0, len(ids)+len(tags))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	tph := make([]string, len(tags))
	for i, t := range tags {
		tph[i] = "?"
		args = append(args, t)
	}
	query := fmt.Sprintf(
		`SELECT message_id, proptag, propval FROM message_properties
		 WHERE message_id IN (%s) AND proptag IN (%s)`,
		strings.Join(ph, ","), strings.Join(tph, ","))
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
		tag := mapi.PropTag(uint32(rawTag))
		val, err := s.loadPropval(tag, col)
		if err != nil {
			return fmt.Errorf("objectstore: decode %s: %w", tag, err)
		}
		sval, _ := val.(string)
		th := out[mid]
		switch tag {
		case mapi.PrInternetMessageID:
			th.MessageID = sval
		case mapi.PrInternetReferences:
			th.References = sval
		case mapi.PrInReplyToID:
			th.InReplyTo = sval
		}
		out[mid] = th
	}
	return rows.Err()
}
