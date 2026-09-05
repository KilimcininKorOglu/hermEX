package directory

import (
	"database/sql"
	"errors"
	"time"
)

// DefaultAutoReplySubjectPrefix is the prefix used when no row has been saved.
// It is the string every out-of-office reply carried before the prefix became
// configurable, so a deployment that never opens the admin form keeps the
// wording it has today. It lives here because both the MTA that sends the reply
// and the admin form that edits it need to name the same default.
const DefaultAutoReplySubjectPrefix = "Automatic reply"

// AutoReplySettings is the out-of-office reply's stored configuration.
// SubjectPrefix is what an auto-reply uses when the mailbox stores no subject of
// its own, which is every mailbox that turned out of office on from EWS or
// ActiveSync, since neither protocol carries a subject field.
type AutoReplySettings struct {
	SubjectPrefix string
}

// GetAutoReplySettings returns the stored auto-reply settings and whether a row
// has been saved. When none has, found is false and the caller keeps the MTA's
// built-in prefix.
func (d *SQLDirectory) GetAutoReplySettings() (AutoReplySettings, bool, error) {
	var s AutoReplySettings
	err := d.db.QueryRow(`SELECT subject_prefix FROM autoreply_settings WHERE id = 1`).
		Scan(&s.SubjectPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return AutoReplySettings{}, false, nil
	}
	if err != nil {
		return AutoReplySettings{}, false, err
	}
	return s, true, nil
}

// SetAutoReplySettings persists the auto-reply settings, upserting the single row
// so the MTA's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetAutoReplySettings(s AutoReplySettings) error {
	_, err := d.db.Exec(
		`INSERT INTO autoreply_settings (id, subject_prefix, updated_at) VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE subject_prefix = VALUES(subject_prefix), updated_at = VALUES(updated_at)`,
		s.SubjectPrefix, time.Now().UnixMilli())
	return err
}
