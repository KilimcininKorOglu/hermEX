package objectstore

import (
	"database/sql"
	"errors"

	"hermex/internal/mapi"
)

// cfgReadStateRepaired marks a store whose appended \Seen messages were marked
// read in the object store.
const cfgReadStateRepaired = 13

// appendMsgFlags maps an append's IMAP flags to the PidTagMessageFlags bits they
// mean: \Seen is read, which CreateMessage stores as the read state, and \Draft
// is unsent, which MAPI clients read as a draft they may edit and send.
func appendMsgFlags(flags int64) int32 {
	var bits int32
	if flags&FlagSeen != 0 {
		bits |= mapi.MsgFlagRead
	}
	if flags&FlagDraft != 0 {
		bits |= mapi.MsgFlagUnsent
	}
	return bits
}

// addMessageFlags sets bits in a message's PidTagMessageFlags.
func addMessageFlags(props *mapi.PropertyValues, bits int32) {
	flags, _ := props.Get(mapi.PrMessageFlags)
	f, _ := flags.(int32)
	props.Set(mapi.PrMessageFlags, f|bits)
}

// repairAppendedReadState marks read, once per store, each message an IMAP
// append stored with \Seen while the object store kept it unread. Such a message
// read as seen over IMAP, POP3 and webmail and as unread over MAPI, EWS and
// ActiveSync. A message whose read state was ever changed carries a read change
// number and is left alone, since its read state then came from a client. Each
// repair takes a read change number, so a client that already synchronized the
// message unread learns that it is read.
func (s *Store) repairAppendedReadState() {
	var done int64
	err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgReadStateRepaired).Scan(&done)
	if err == nil && done == 1 {
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logStoreError("repair_read_state", err)
		return
	}
	if err := s.markAppendedSeenRead(); err != nil {
		s.logStoreError("repair_read_state", err)
		return
	}
	if _, err := s.objdb.Exec(`REPLACE INTO configurations (config_id, config_value) VALUES (?, 1)`, cfgReadStateRepaired); err != nil {
		s.logStoreError("repair_read_state", err)
	}
}

// cfgDraftRepaired marks a store whose appended \Draft messages were marked
// unsent.
const cfgDraftRepaired = 14

// repairAppendedDrafts marks unsent, once per store, each message an append
// stored with \Draft while its PidTagMessageFlags said it was sent. A MAPI client
// showed such a draft as a sent message it could not edit. The write takes a
// change number, so a synchronized client downloads the corrected flags.
func (s *Store) repairAppendedDrafts() {
	var done int64
	err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgDraftRepaired).Scan(&done)
	if err == nil && done == 1 {
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logStoreError("repair_drafts", err)
		return
	}
	if err := s.markAppendedDraftsUnsent(); err != nil {
		s.logStoreError("repair_drafts", err)
		return
	}
	if _, err := s.objdb.Exec(`REPLACE INTO configurations (config_id, config_value) VALUES (?, 1)`, cfgDraftRepaired); err != nil {
		s.logStoreError("repair_drafts", err)
	}
}

// markAppendedDraftsUnsent sets the unsent bit on every message the index records
// as a draft and whose stored flags lack it.
func (s *Store) markAppendedDraftsUnsent() error {
	rows, err := s.idxdb.Query(`SELECT message_id FROM messages WHERE unsent=1`)
	if err != nil {
		return err
	}
	drafts, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range drafts {
		props, err := s.GetMessageProperties(id, mapi.PrMessageFlags)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		v, _ := props.Get(mapi.PrMessageFlags)
		f, _ := v.(int32)
		if f&mapi.MsgFlagUnsent != 0 {
			continue
		}
		if err := s.ModifyMessageProperties(id, mapi.PropertyValues{{Tag: mapi.PrMessageFlags, Value: f | mapi.MsgFlagUnsent}}); err != nil {
			return err
		}
	}
	return nil
}

// markAppendedSeenRead marks read every message the index records as seen and
// the object store as unread with no read change.
func (s *Store) markAppendedSeenRead() error {
	rows, err := s.idxdb.Query(`SELECT message_id FROM messages WHERE read=1`)
	if err != nil {
		return err
	}
	seen, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range seen {
		var unread int
		err := s.objdb.QueryRow(`SELECT 1 FROM messages WHERE message_id=? AND read_state=0 AND read_cn IS NULL`, id).Scan(&unread)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := s.setObjReadState(id, 1); err != nil {
			return err
		}
	}
	return nil
}
