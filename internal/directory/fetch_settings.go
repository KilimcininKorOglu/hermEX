package directory

import (
	"database/sql"
	"errors"
	"time"
)

// FetchSettings is the fetch worker's stored source policy. AllowInternalSources
// decides whether a configured POP3/IMAP source may resolve to a loopback,
// link-local or private address. It is deliberately a system-wide switch rather
// than a per-entry flag: a fetchmail entry is created by a domain-scoped admin,
// so a per-entry opt-in would let the very role the block exists to contain turn
// the block off.
type FetchSettings struct {
	AllowInternalSources bool
}

// GetFetchSettings returns the stored fetch policy and whether a row has been
// saved. When none has, found is false and the worker keeps its built-in default
// (internal sources refused).
func (d *SQLDirectory) GetFetchSettings() (FetchSettings, bool, error) {
	var s FetchSettings
	err := d.db.QueryRow(`SELECT allow_internal_sources FROM fetch_settings WHERE id = 1`).
		Scan(&s.AllowInternalSources)
	if errors.Is(err, sql.ErrNoRows) {
		return FetchSettings{}, false, nil
	}
	if err != nil {
		return FetchSettings{}, false, err
	}
	return s, true, nil
}

// SetFetchSettings persists the fetch policy, upserting the single row so the
// worker observes the change on its next poll and applies it without a restart.
func (d *SQLDirectory) SetFetchSettings(s FetchSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO fetch_settings (id, allow_internal_sources, updated_at)
		 VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE allow_internal_sources = VALUES(allow_internal_sources),
		   updated_at = VALUES(updated_at)`,
		s.AllowInternalSources, time.Now().UnixMilli())
	return err
}
