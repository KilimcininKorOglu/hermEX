package directory

import (
	"database/sql"
	"errors"
	"time"
)

// GetGreylistEnabled reports whether greylisting is turned on; it defaults to off
// when no row has been saved.
func (d *SQLDirectory) GetGreylistEnabled() (bool, error) {
	var enabled bool
	err := d.db.QueryRow(`SELECT enabled FROM greylist_settings WHERE id = 1`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

// SetGreylistEnabled turns greylisting on or off. It upserts the single row so the
// MTA's poll observes the change and applies it without a restart. It touches only the
// enabled column, so the timings (edited separately) are preserved.
func (d *SQLDirectory) SetGreylistEnabled(on bool) error {
	_, err := d.db.Exec(
		`INSERT INTO greylist_settings (id, enabled, updated_at) VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), updated_at = VALUES(updated_at)`,
		on, time.Now().UnixMilli())
	return err
}

// GreylistTimings is the operator-editable greylist timing, all in seconds: the
// minimum delay before a first-seen triplet is accepted, and the TTLs for unconfirmed
// and confirmed triplets. The enable toggle is stored in the same row but edited
// separately (GetGreylistEnabled/SetGreylistEnabled).
type GreylistTimings struct {
	MinDelay       int64
	UnconfirmedTTL int64
	ConfirmedTTL   int64
}

// GetGreylistTimings returns the stored greylist timings and whether a row has been
// saved. When none has, found is false and the caller keeps the greylister's built-in
// default timings.
func (d *SQLDirectory) GetGreylistTimings() (GreylistTimings, bool, error) {
	var t GreylistTimings
	err := d.db.QueryRow(
		`SELECT min_delay, unconfirmed_ttl, confirmed_ttl FROM greylist_settings WHERE id = 1`).
		Scan(&t.MinDelay, &t.UnconfirmedTTL, &t.ConfirmedTTL)
	if errors.Is(err, sql.ErrNoRows) {
		return GreylistTimings{}, false, nil
	}
	if err != nil {
		return GreylistTimings{}, false, err
	}
	return t, true, nil
}

// SetGreylistTimings persists the greylist timings, upserting the single row so the
// MTA's poll observes the change and applies it without a restart. It touches only the
// timing columns, so the enable toggle (edited separately) is preserved.
func (d *SQLDirectory) SetGreylistTimings(t GreylistTimings) error {
	_, err := d.db.Exec(
		`INSERT INTO greylist_settings (id, min_delay, unconfirmed_ttl, confirmed_ttl, updated_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE min_delay = VALUES(min_delay), unconfirmed_ttl = VALUES(unconfirmed_ttl),
		   confirmed_ttl = VALUES(confirmed_ttl), updated_at = VALUES(updated_at)`,
		t.MinDelay, t.UnconfirmedTTL, t.ConfirmedTTL, time.Now().UnixMilli())
	return err
}
