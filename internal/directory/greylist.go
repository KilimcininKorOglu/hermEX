package directory

import (
	"database/sql"
	"errors"
)

// Greylisted is the stored state of one greylist triplet: when it was first seen
// and whether it has been confirmed (a retry passed the delay).
type Greylisted struct {
	FirstSeen int64
	Confirmed bool
}

// GreylistGet returns a triplet's state; found is false when it has never been
// seen. ipKey is the sender's masked network; sender and recipient are normalised
// by the caller.
func (d *SQLDirectory) GreylistGet(ipKey, sender, recipient string) (Greylisted, bool, error) {
	var g Greylisted
	err := d.db.QueryRow(
		`SELECT first_seen, confirmed FROM greylist_triplets WHERE ip_key = ? AND sender = ? AND recipient = ?`,
		ipKey, sender, recipient).Scan(&g.FirstSeen, &g.Confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return Greylisted{}, false, nil
	}
	if err != nil {
		return Greylisted{}, false, err
	}
	return g, true, nil
}

// GreylistUpsertSeen records a first contact (or refreshes an existing triplet's
// last-seen time) without confirming it.
func (d *SQLDirectory) GreylistUpsertSeen(ipKey, sender, recipient string, now int64) error {
	_, err := d.db.Exec(
		`INSERT INTO greylist_triplets (ip_key, sender, recipient, first_seen, last_seen, confirmed)
		 VALUES (?, ?, ?, ?, ?, 0)
		 ON DUPLICATE KEY UPDATE last_seen = VALUES(last_seen)`,
		ipKey, sender, recipient, now, now)
	return err
}

// GreylistConfirm marks a triplet confirmed (a retry passed the delay) so future
// mail from it is accepted without deferral.
func (d *SQLDirectory) GreylistConfirm(ipKey, sender, recipient string, now int64) error {
	_, err := d.db.Exec(
		`UPDATE greylist_triplets SET confirmed = 1, last_seen = ? WHERE ip_key = ? AND sender = ? AND recipient = ?`,
		now, ipKey, sender, recipient)
	return err
}

// PruneGreylist bounds the table: it removes unconfirmed triplets first seen before
// unconfirmedBefore (a deferral that never retried) and confirmed triplets not seen
// since confirmedBefore (a stale allowlisting).
func (d *SQLDirectory) PruneGreylist(unconfirmedBefore, confirmedBefore int64) error {
	_, err := d.db.Exec(
		`DELETE FROM greylist_triplets WHERE (confirmed = 0 AND first_seen < ?) OR (confirmed = 1 AND last_seen < ?)`,
		unconfirmedBefore, confirmedBefore)
	return err
}
