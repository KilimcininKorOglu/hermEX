package objectstore

import (
	"database/sql"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// CreateMessage inserts a MAPI message object into a folder: it allocates the
// message EID from the folder's range and a fresh change number, writes the
// denormalized message row, then the top-level property bag, one recipient row
// per recipient bag, one attachment row per attachment bag, and the time-sort
// index entry — all in a single transaction so the message and everything it
// owns commit atomically. Content properties (bodies, attachment payloads) are
// offloaded to content files by the property layer. It returns the new message
// EID.
func (s *Store) CreateMessage(folderID int64, msg *oxcmail.Message) (int64, error) {
	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	eid, err := allocateEIDFromFolder(tx, folderID)
	if err != nil {
		return 0, err
	}
	cn, err := allocateCN(tx)
	if err != nil {
		return 0, err
	}
	mid := midString(eid)
	id := int64(eid)

	if _, err := tx.Exec(
		`INSERT INTO messages
		   (message_id, parent_fid, is_associated, change_number, read_state, message_size, mid_string)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, folderID, isAssociated(msg.Props), int64(cn), readState(msg.Props), messageSize(msg), mid); err != nil {
		return 0, err
	}

	if err := s.insertProps(tx, "message_properties", "message_id", id, msg.Props); err != nil {
		return 0, err
	}

	// Every message carries PidTagMessageStatus so the status ROPs can read and
	// modify it; the reference forces it present at write time. Seed 0 when the
	// caller did not supply one (status is otherwise managed via RopSetMessageStatus).
	if _, ok := msg.Props.Get(mapi.PrMsgStatus); !ok {
		if err := s.insertProps(tx, "message_properties", "message_id", id,
			mapi.PropertyValues{{Tag: mapi.PrMsgStatus, Value: int32(0)}}); err != nil {
			return 0, err
		}
	}

	for _, rcpt := range msg.Recipients {
		res, err := tx.Exec(`INSERT INTO recipients (message_id) VALUES (?)`, id)
		if err != nil {
			return 0, err
		}
		rid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err := s.insertProps(tx, "recipients_properties", "recipient_id", rid, rcpt); err != nil {
			return 0, err
		}
	}

	for i, att := range msg.Attachments {
		res, err := tx.Exec(`INSERT INTO attachments (message_id) VALUES (?)`, id)
		if err != nil {
			return 0, err
		}
		aid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		// Assign a stable per-message attach number (PidTagAttachNumber) when the
		// source did not carry one — mail import does not, an ICS upload does. The
		// number is the ordinal here, matching the 0-based sequence CreateAttachment
		// continues, so the read path can resolve an attachment by it rather than by
		// a position that shifts on a sibling delete.
		aprops := att.Props
		if _, ok := aprops.Get(mapi.PrAttachNum); !ok {
			aprops = append(mapi.PropertyValues(nil), att.Props...)
			aprops.Set(mapi.PrAttachNum, int32(i))
		}
		if err := s.insertProps(tx, "attachment_properties", "attachment_id", aid, aprops); err != nil {
			return 0, err
		}
	}

	if err := insertMsgTime(tx, folderID, id, msg.Props); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// Wake long-poll consumers that diff the object store (MAPI/EWS). A message
	// reaches the IMAP index only via AppendMessage, which re-publishes after it
	// indexes, so an IMAP consumer is woken once the row it can see exists.
	s.publishChange("create", cn, mid)
	return id, nil
}

// midString derives a message's mid_string — its eml filename and the bridge
// between the object store and the IMAP index — from its EID. The EID is unique
// per mailbox, so the derived id is too, and it is stable for the message's
// life. The "m" prefix keeps it textually distinct from any bare-numeric id.
func midString(eid uint64) string {
	return "m" + strconv.FormatUint(eid, 10)
}

// isAssociated reports the stored associated flag for a new message: a message
// is folder-associated (FAI — a hidden setting/rule/form, not a visible item)
// when it carries PidTagAssociated set true. The flag is fixed at creation; the
// contents-table query splits associated from normal messages by this column.
func isAssociated(props mapi.PropertyValues) int {
	if v, ok := props.Get(mapi.PrAssociated); ok {
		if b, ok := v.(bool); ok && b {
			return 1
		}
	}
	return 0
}

// readState reports the stored read flag for a new message: delivered mail is
// unread unless the message explicitly carries the read message flag.
func readState(props mapi.PropertyValues) int {
	if v, ok := props.Get(mapi.PrMessageFlags); ok {
		if f, ok := v.(int32); ok && f&mapi.MsgFlagRead != 0 {
			return 1
		}
	}
	return 0
}

// messageSize approximates the MAPI message size (PidTagMessageSize): the total
// bytes of every property value across the message, its recipients, and its
// attachments. It feeds quota accounting and is independent of the RFC822 wire
// size the IMAP index records for each message.
func messageSize(msg *oxcmail.Message) int64 {
	n := propsSize(msg.Props)
	for _, r := range msg.Recipients {
		n += propsSize(r)
	}
	for _, a := range msg.Attachments {
		n += propsSize(a.Props)
	}
	return n
}

func propsSize(pv mapi.PropertyValues) int64 {
	var n int64
	for _, p := range pv {
		n += valueSize(p.Value)
	}
	return n
}

// valueSize is the stored byte size of a property value for size accounting.
func valueSize(v any) int64 {
	switch x := v.(type) {
	case string:
		return int64(len(x))
	case []byte:
		return int64(len(x))
	case bool:
		return 1
	case int16:
		return 2
	case int32, uint32, float32:
		return 4
	case int64, uint64, float64:
		return 8
	default:
		return 0
	}
}

// insertMsgTime records a message's time positions for time-sorted folder
// views: modification (now), received (delivery), and sent (submit). Times are
// NT timestamps, matching how they are stored as properties; a missing time is
// recorded as 0.
func insertMsgTime(tx *sql.Tx, folderID, messageID int64, props mapi.PropertyValues) error {
	_, err := tx.Exec(
		`INSERT INTO msgtime_index (folder_id, message_id, mtime, rcvtime, sndtime)
		 VALUES (?, ?, ?, ?, ?)`,
		folderID, messageID,
		int64(mapi.UnixToNTTime(time.Now())),
		int64(ntProp(props, mapi.PrMessageDeliveryTime)),
		int64(ntProp(props, mapi.PrClientSubmitTime)))
	return err
}

// ntProp reads an NT-timestamp property, returning 0 when absent.
func ntProp(pv mapi.PropertyValues, tag mapi.PropTag) uint64 {
	if v, ok := pv.Get(tag); ok {
		if t, ok := v.(uint64); ok {
			return t
		}
	}
	return 0
}
