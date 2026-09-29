package objectstore

import (
	"database/sql"
	"errors"

	"hermex/internal/mapi"
)

// cfgReadStateRepaired marks a store whose appended \Seen messages were marked
// read in the object store.
const cfgReadStateRepaired = 13

// markReadProp sets the read bit of a new message's PidTagMessageFlags, which is
// what CreateMessage stores as its read state.
func markReadProp(props *mapi.PropertyValues) {
	flags, _ := props.Get(mapi.PrMessageFlags)
	f, _ := flags.(int32)
	props.Set(mapi.PrMessageFlags, f|mapi.MsgFlagRead)
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
