package directory

// SpamVerdict is one inbound message's spam-scoring outcome, recorded for the
// admin Spam History view. Time is unix seconds; Reasons is the joined list of
// signals that fired.
type SpamVerdict struct {
	ID         int64
	Time       int64
	MailFrom   string
	RemoteAddr string
	Score      int
	Spam       bool
	Reasons    string
}

// defaultSpamHistoryRetain is the built-in retention bound used until an operator
// sets one from the admin panel. Because AUTO_INCREMENT ids are monotonic, pruning
// rows at or below (newest id - retain) on each insert keeps roughly the newest
// retain rows, so the history never grows unbounded. NewSQL seeds the directory's
// runtime bound with this value; SetSpamHistoryRetain replaces it.
const defaultSpamHistoryRetain int64 = 10000

// SetSpamHistoryRetain updates the runtime retention bound RecordSpamVerdict
// enforces. The MTA's poll calls it so an operator's edit applies without a restart.
// A value below 1 is ignored, so a misconfiguration can never prune the table to
// nothing.
func (d *SQLDirectory) SetSpamHistoryRetain(n int64) {
	if n < 1 {
		return
	}
	d.spamRetain.Store(n)
}

// RecordSpamVerdict appends one scored message's outcome and prunes the table to
// the retention cap. The MTA calls it fail-open — a delivery must never fail
// because history could not be written — so the caller logs and ignores any error.
func (d *SQLDirectory) RecordSpamVerdict(v SpamVerdict) error {
	if len(v.Reasons) > 512 {
		v.Reasons = v.Reasons[:512]
	}
	res, err := d.db.Exec(
		`INSERT INTO spam_history (ts, mail_from, remote_addr, score, spam, reasons)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		v.Time, v.MailFrom, v.RemoteAddr, v.Score, v.Spam, v.Reasons)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		// Best-effort retention prune; a failure here must not fail the record.
		_, _ = d.db.Exec(`DELETE FROM spam_history WHERE id <= ?`, id-d.spamRetain.Load())
	}
	return nil
}

// RecentSpamVerdicts returns the most recent verdicts, newest first, capped at
// limit (default 200 when limit <= 0).
func (d *SQLDirectory) RecentSpamVerdicts(limit int) ([]SpamVerdict, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := d.db.Query(
		`SELECT id, ts, mail_from, remote_addr, score, spam, reasons
		   FROM spam_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpamVerdict
	for rows.Next() {
		var v SpamVerdict
		if err := rows.Scan(&v.ID, &v.Time, &v.MailFrom, &v.RemoteAddr, &v.Score, &v.Spam, &v.Reasons); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
