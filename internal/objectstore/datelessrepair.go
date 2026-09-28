package objectstore

import (
	"database/sql"
	"errors"
	"strings"

	"hermex/internal/mapi"
)

// cfgDatelessRepaired marks a store whose Date-less messages were re-dated.
const cfgDatelessRepaired = 12

// datelessSlack is how far a submit time may run ahead of the delivery time
// before the message is checked: a message imported without a Date header got a
// submit time a few milliseconds after its delivery, and a sender's clock may run
// a little fast, so only a larger gap marks a message stored by an append that
// named an earlier internal date. It is in NT ticks (100 ns), one minute.
const datelessSlack = 60 * 10_000_000

// repairDatelessMessages re-dates, once per store, each message that arrived
// without a Date header and was dated by the time it was stored instead of by
// its arrival. Such a message shows the store time as its Date on every protocol
// and a different day in a list sorted by arrival. A message that cannot be
// repaired is recorded and the pass runs again on the next open.
func (s *Store) repairDatelessMessages() {
	var done int64
	err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgDatelessRepaired).Scan(&done)
	if err == nil && done == 1 {
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logStoreError("repair_dateless", err)
		return
	}
	if !s.repairDatelessCandidates() {
		return
	}
	if _, err := s.objdb.Exec(`REPLACE INTO configurations (config_id, config_value) VALUES (?, 1)`, cfgDatelessRepaired); err != nil {
		s.logStoreError("repair_dateless", err)
	}
}

// repairDatelessCandidates repairs every candidate and reports whether all of
// them were handled.
func (s *Store) repairDatelessCandidates() bool {
	cands, err := s.datelessCandidates()
	if err != nil {
		s.logStoreError("repair_dateless", err)
		return false
	}
	ok := true
	for _, c := range cands {
		if err := s.repairDateless(c); err != nil {
			s.logStoreError("repair_dateless_message", err)
			ok = false
		}
	}
	return ok
}

// datelessCandidate is a live message whose submit time runs well ahead of its
// delivery time.
type datelessCandidate struct {
	id       int64
	delivery uint64
}

// datelessCandidates lists the live messages whose submit time is later than
// their delivery time by more than datelessSlack.
func (s *Store) datelessCandidates() ([]datelessCandidate, error) {
	rows, err := s.objdb.Query(`SELECT m.message_id, CAST(d.propval AS INTEGER)
		FROM messages m
		JOIN message_properties p ON p.message_id = m.message_id AND p.proptag = ?
		JOIN message_properties d ON d.message_id = m.message_id AND d.proptag = ?
		WHERE m.is_deleted = 0 AND CAST(p.propval AS INTEGER) > CAST(d.propval AS INTEGER) + ?`,
		int64(mapi.PrClientSubmitTime), int64(mapi.PrMessageDeliveryTime), datelessSlack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []datelessCandidate
	for rows.Next() {
		var c datelessCandidate
		var delivery int64
		if err := rows.Scan(&c.id, &delivery); err != nil {
			return nil, err
		}
		// #nosec G115 -- an NT timestamp stored in SQLite's signed 64-bit column; the bits round-trip exactly
		c.delivery = uint64(delivery)
		out = append(out, c)
	}
	return out, rows.Err()
}

// repairDateless re-dates one candidate by its delivery time when the header
// block it arrived with carries no Date. A message that had a Date keeps it.
func (s *Store) repairDateless(c datelessCandidate) error {
	pv, err := s.GetMessageProperties(c.id, mapi.PrTransportMessageHeaders)
	if err != nil {
		return err
	}
	v, _ := pv.Get(mapi.PrTransportMessageHeaders)
	headers, ok := v.(string)
	if !ok || headers == "" || hasDateHeader(headers) {
		return nil
	}
	return s.ModifyMessageProperties(c.id, mapi.PropertyValues{
		{Tag: mapi.PrClientSubmitTime, Value: c.delivery},
		{Tag: mapi.PrCreationTime, Value: c.delivery},
	})
}

// hasDateHeader reports whether a header block names a Date field. Only a field
// name at the start of a line counts, so a folded continuation or a body line
// that mentions "Date:" does not.
func hasDateHeader(headers string) bool {
	for line := range strings.SplitSeq(headers, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			return false
		}
		name, _, found := strings.Cut(line, ":")
		if found && strings.EqualFold(name, "Date") {
			return true
		}
	}
	return false
}
