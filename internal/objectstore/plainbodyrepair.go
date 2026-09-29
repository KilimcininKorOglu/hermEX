package objectstore

import (
	"database/sql"
	"errors"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// cfgPlainBodyRepaired marks a store whose HTML-only messages were given the
// plain body the import now derives from their HTML.
const cfgPlainBodyRepaired = 15

// repairPlainBodies gives, once per store, each message stored with an HTML body
// and no plain body the PR_BODY the import now derives from that HTML, so an
// inbox rule or a search folder testing the body text matches it. The text is
// derived from content the message already holds, so the write takes no change
// number and leaves the served bytes alone: an IMAP client that cached the
// message keeps the same bytes under the same UID.
func (s *Store) repairPlainBodies() {
	var done int64
	err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgPlainBodyRepaired).Scan(&done)
	if err == nil && done == 1 {
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logStoreError("repair_plain_body", err)
		return
	}
	if err := s.fillPlainBodies(); err != nil {
		s.logStoreError("repair_plain_body", err)
		return
	}
	if _, err := s.objdb.Exec(`REPLACE INTO configurations (config_id, config_value) VALUES (?, 1)`, cfgPlainBodyRepaired); err != nil {
		s.logStoreError("repair_plain_body", err)
	}
}

// fillPlainBodies writes the derived plain body on every message that has an
// HTML body and no plain body.
func (s *Store) fillPlainBodies() error {
	rows, err := s.objdb.Query(
		`SELECT h.message_id FROM message_properties h WHERE h.proptag=?
		 AND NOT EXISTS (SELECT 1 FROM message_properties b WHERE b.message_id=h.message_id AND b.proptag IN (?, ?))`,
		int64(uint32(mapi.PrHTML)), int64(uint32(mapi.PrBody)), int64(uint32(mapi.PrBody.WithType(mapi.PtString8))))
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, id := range ids {
		props, err := s.GetMessageProperties(id, mapi.PrHTML, mapi.PrInternetCodepage)
		if err != nil {
			return err
		}
		text := oxcmail.PlainBodyFromHTML(props)
		if text == "" {
			continue
		}
		if err := s.setObjectProps("message_properties", "message_id", id, mapi.PropertyValues{{Tag: mapi.PrBody, Value: text}}); err != nil {
			return err
		}
		s.refreshMessageSize(id)
	}
	return nil
}
