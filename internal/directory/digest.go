package directory

import (
	"database/sql"
	"errors"
	"time"
)

// DigestSettings is the quarantine-digest configuration: whether the periodic summary
// runs, how often (in hours), and the externally-reachable base URL its release links
// are built from (e.g. "https://mail.example.com").
type DigestSettings struct {
	Enabled       bool
	IntervalHours int
	BaseURL       string
}

// GetDigestSettings returns the stored digest settings and whether a row has been
// saved. When none has, found is false and the caller keeps the worker's built-in
// defaults (disabled).
func (d *SQLDirectory) GetDigestSettings() (DigestSettings, bool, error) {
	var s DigestSettings
	err := d.db.QueryRow(
		`SELECT enabled, interval_hours, base_url FROM digest_settings WHERE id = 1`).
		Scan(&s.Enabled, &s.IntervalHours, &s.BaseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return DigestSettings{}, false, nil
	}
	if err != nil {
		return DigestSettings{}, false, err
	}
	return s, true, nil
}

// SetDigestSettings persists the digest settings, upserting the single row so the MTA
// observes the change on its next poll.
func (d *SQLDirectory) SetDigestSettings(s DigestSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO digest_settings (id, enabled, interval_hours, base_url, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), interval_hours = VALUES(interval_hours),
		   base_url = VALUES(base_url), updated_at = VALUES(updated_at)`,
		s.Enabled, s.IntervalHours, s.BaseURL, time.Now().UnixMilli())
	return err
}

// GetDigestWatermark returns the highest Junk UID already summarized for a mailbox, or
// 0 when that mailbox has never been digested. Because Junk UIDs only ever increase, a
// message with a higher UID is one that arrived since the last digest.
func (d *SQLDirectory) GetDigestWatermark(maildir string) (uint32, error) {
	var uid uint32
	err := d.db.QueryRow(`SELECT last_uid FROM digest_state WHERE maildir = ?`, maildir).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return uid, err
}

// SetDigestWatermark records the highest Junk UID summarized for a mailbox, so the next
// run includes only newer messages.
func (d *SQLDirectory) SetDigestWatermark(maildir string, uid uint32) error {
	_, err := d.db.Exec(
		`INSERT INTO digest_state (maildir, last_uid, updated_at) VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE last_uid = VALUES(last_uid), updated_at = VALUES(updated_at)`,
		maildir, uid, time.Now().UnixMilli())
	return err
}
