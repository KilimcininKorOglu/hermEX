package objectstore

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// recomputedMsgFlags are the PidTagMessageFlags bits the store derives on every
// read rather than keeps ([MS-OXCMSG] 2.2.1.6): the read state, the paperclip, the
// FAI bit and the pending read and non-read receipts. A stored value supplies only
// the other bits, so a client write can never make them disagree with the message.
const recomputedMsgFlags = mapi.MsgFlagRead | mapi.MsgFlagHasAttach | mapi.MsgFlagAssociated |
	mapi.MsgFlagRNPending | mapi.MsgFlagNRNPending

// ComputedMessageTags are the message properties MessageComputedProps derives.
var ComputedMessageTags = []mapi.PropTag{mapi.PrMessageFlags, mapi.PrRead, mapi.PrHasAttachments}

// StripComputedMsgFlags returns a PidTagMessageFlags value a client writes with the
// derived bits cleared, since the store recomputes them on every read.
func StripComputedMsgFlags(flags int32) int32 {
	return flags &^ recomputedMsgFlags
}

// openTransferMessage opens a message for a FastTransfer stream, a copy or an
// ICS download, carrying its computed PidTagMessageFlags, so the receiver learns
// the read state and the paperclip as a property read would give them.
func (s *Store) openTransferMessage(messageID int64) (*oxcmail.Message, error) {
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		return nil, err
	}
	computed, err := s.MessageComputedProps(messageID, mapi.PrMessageFlags)
	if err != nil {
		return nil, err
	}
	for _, tv := range computed {
		msg.Props.Set(tv.Tag, tv.Value)
	}
	return msg, nil
}

// MessageComputedProps returns the requested message properties the store
// computes rather than stores: PidTagMessageFlags, PidTagRead and
// PidTagHasAttachments. An empty tag list asks for all of them. A message that
// does not exist gets no properties.
func (s *Store) MessageComputedProps(messageID int64, tags ...mapi.PropTag) (mapi.PropertyValues, error) {
	byID, err := s.MessageComputedPropsBatch([]int64{messageID}, tags)
	if err != nil {
		return nil, err
	}
	return byID[messageID], nil
}

// MessageComputedPropsBatch is MessageComputedProps for many messages, one query
// per chunk of ids. A message that does not exist is absent from the result.
func (s *Store) MessageComputedPropsBatch(ids []int64, tags []mapi.PropTag) (map[int64]mapi.PropertyValues, error) {
	out := make(map[int64]mapi.PropertyValues, len(ids))
	if !wantsComputedMessageProps(tags) {
		return out, nil
	}
	chunk := batchIDChunk(5)
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		if err := s.computedMessageChunk(ids[start:end], tags, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// wantsComputedMessageProps reports whether a read asks for any computed message
// property; an empty request asks for all.
func wantsComputedMessageProps(tags []mapi.PropTag) bool {
	return len(tags) == 0 || slices.ContainsFunc(ComputedMessageTags, func(t mapi.PropTag) bool { return slices.Contains(tags, t) })
}

// messageFlagFacts are the stored facts the computed message properties are
// derived from.
type messageFlagFacts struct {
	read, associated, hasAttach bool
	stored, rn, nrn             sql.NullInt64
}

// computedMessageChunk reads one bounded batch of ids into out.
func (s *Store) computedMessageChunk(ids []int64, tags []mapi.PropTag, out map[int64]mapi.PropertyValues) error {
	args := []any{
		int64(uint32(mapi.PrMessageFlags)), int64(uint32(mapi.PrReadReceiptRequested)),
		int64(uint32(mapi.PrNonReceiptNotificationRequested)),
		int64(uint32(mapi.PrAttachFlags)), int64(mapi.AttMhtmlRef),
	}
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	// The attachment test is HasAttachments' own: an inline MHTML reference is not
	// a paperclip.
	// #nosec G201 -- the formatted part is a run of ? placeholders; every value travels as a query argument
	query := fmt.Sprintf(
		`SELECT m.message_id, m.read_state, COALESCE(m.is_associated, 0),
		   (SELECT propval FROM message_properties WHERE message_id=m.message_id AND proptag=?),
		   (SELECT propval FROM message_properties WHERE message_id=m.message_id AND proptag=?),
		   (SELECT propval FROM message_properties WHERE message_id=m.message_id AND proptag=?),
		   EXISTS(SELECT 1 FROM attachments a WHERE a.message_id=m.message_id
		     AND NOT EXISTS(SELECT 1 FROM attachment_properties ap
		       WHERE ap.attachment_id=a.attachment_id AND ap.proptag=? AND (ap.propval & ?) <> 0))
		 FROM messages m WHERE m.message_id IN (%s)`, strings.Join(ph, ","))
	rows, err := s.objdb.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var f messageFlagFacts
		if err := rows.Scan(&id, &f.read, &f.associated, &f.stored, &f.rn, &f.nrn, &f.hasAttach); err != nil {
			return err
		}
		out[id] = f.props(tags)
	}
	return rows.Err()
}

// props derives the requested computed properties from the stored facts.
func (f messageFlagFacts) props(tags []mapi.PropTag) mapi.PropertyValues {
	all := mapi.PropertyValues{
		{Tag: mapi.PrMessageFlags, Value: f.flags()},
		{Tag: mapi.PrRead, Value: f.read},
		{Tag: mapi.PrHasAttachments, Value: f.hasAttach},
	}
	if len(tags) == 0 {
		return all
	}
	return slices.DeleteFunc(all, func(pv mapi.TaggedPropVal) bool { return !slices.Contains(tags, pv.Tag) })
}

// flags is the message's PidTagMessageFlags: the stored bits the store does not
// derive, with the derived ones set from the message.
func (f messageFlagFacts) flags() int32 {
	// #nosec G115 -- a PtLong is stored as the int64 widening of the same 32 bits
	v := StripComputedMsgFlags(int32(f.stored.Int64))
	set := func(bit int32, on bool) {
		if on {
			v |= bit
		}
	}
	set(mapi.MsgFlagRead, f.read)
	set(mapi.MsgFlagHasAttach, f.hasAttach)
	set(mapi.MsgFlagAssociated, f.associated)
	set(mapi.MsgFlagRNPending, f.rn.Valid && f.rn.Int64 != 0)
	set(mapi.MsgFlagNRNPending, f.nrn.Valid && f.nrn.Int64 != 0)
	return v
}
