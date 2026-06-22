package directory

import (
	"database/sql"
	"errors"
)

// SpamThresholdForMaildir resolves the per-recipient spam-threshold override for the
// mailbox at maildir: the user's own override if set, otherwise their domain's
// override if set. ok is false when neither is set, so the caller falls back to the
// global threshold. The maildir uniquely identifies the user, so this resolves
// correctly for an alias recipient too (the alias shares the owner's mailbox). It is
// fail-open: the caller treats an error as no override.
func (d *SQLDirectory) SpamThresholdForMaildir(maildir string) (threshold int, ok bool, err error) {
	var user, domain sql.NullInt64
	err = d.db.QueryRow(
		`SELECT u.spam_threshold, dm.spam_threshold
		   FROM users u JOIN domains dm ON u.domain_id = dm.id
		  WHERE u.maildir = ?`, maildir).Scan(&user, &domain)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if user.Valid {
		return int(user.Int64), true, nil
	}
	if domain.Valid {
		return int(domain.Int64), true, nil
	}
	return 0, false, nil
}

// GetUserSpamThreshold returns a user's spam-threshold override, or nil when the user
// inherits (the column is NULL or the user is unknown).
func (d *SQLDirectory) GetUserSpamThreshold(username string) (*int, error) {
	return scanThreshold(d.db.QueryRow(`SELECT spam_threshold FROM users WHERE username = ?`, username))
}

// SetUserSpamThreshold sets or clears a user's spam-threshold override. A nil
// threshold clears it so the user inherits the domain or global threshold.
func (d *SQLDirectory) SetUserSpamThreshold(username string, threshold *int) error {
	_, err := d.db.Exec(`UPDATE users SET spam_threshold = ? WHERE username = ?`, nullableInt(threshold), username)
	return err
}

// GetDomainSpamThreshold returns a domain's spam-threshold override, or nil when the
// domain inherits (the column is NULL or the domain is unknown).
func (d *SQLDirectory) GetDomainSpamThreshold(domain string) (*int, error) {
	return scanThreshold(d.db.QueryRow(`SELECT spam_threshold FROM domains WHERE domainname = ?`, domain))
}

// SetDomainSpamThreshold sets or clears a domain's spam-threshold override. A nil
// threshold clears it so the domain inherits the global threshold.
func (d *SQLDirectory) SetDomainSpamThreshold(domain string, threshold *int) error {
	_, err := d.db.Exec(`UPDATE domains SET spam_threshold = ? WHERE domainname = ?`, nullableInt(threshold), domain)
	return err
}

// scanThreshold reads a nullable spam_threshold column into *int (nil when NULL or no
// row).
func scanThreshold(row *sql.Row) (*int, error) {
	var v sql.NullInt64
	err := row.Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !v.Valid {
		return nil, nil
	}
	n := int(v.Int64)
	return &n, nil
}

// nullableInt maps a *int to a value for a nullable SQL column: NULL when nil.
func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
