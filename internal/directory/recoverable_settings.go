package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// DefaultRecoverableRetentionDays is the Exchange-matching retention window applied
// when no row has been saved.
const DefaultRecoverableRetentionDays = 14

// RecoverableSettings is the operator-editable Recoverable Items retention window,
// in days (0 disables auto-purge). It is stored as a single row; a sweep polls it
// and applies a change without a restart.
type RecoverableSettings struct {
	RetentionDays int
}

// GetRecoverableSettings returns the stored retention window and whether a row has
// been saved. When none has, found is false and the caller uses the default.
func (d *SQLDirectory) GetRecoverableSettings() (RecoverableSettings, bool, error) {
	var s RecoverableSettings
	err := d.db.QueryRow(
		`SELECT retention_days FROM recoverable_settings WHERE id = 1`).Scan(&s.RetentionDays)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoverableSettings{}, false, nil
	}
	if err != nil {
		return RecoverableSettings{}, false, err
	}
	return s, true, nil
}

// SetRecoverableSettings persists the retention window, upserting the single row so
// the sweep's poll observes the change and applies it without a restart.
func (d *SQLDirectory) SetRecoverableSettings(s RecoverableSettings) error {
	if s.RetentionDays < 0 {
		return fmt.Errorf("directory: recoverable-items retention %d is negative; 0 keeps items forever", s.RetentionDays)
	}
	_, err := d.db.Exec(
		`INSERT INTO recoverable_settings (id, retention_days, updated_at)
		 VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE retention_days = VALUES(retention_days), updated_at = VALUES(updated_at)`,
		s.RetentionDays, time.Now().UnixMilli())
	return err
}

// AllMaildirs returns every user's mailbox directory, skipping accounts without one
// (contacts and similar non-login users carry an empty maildir).
func (d *SQLDirectory) AllMaildirs() (out []string, err error) {
	rows, err := d.db.Query(`SELECT maildir FROM users WHERE maildir <> ''`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	for rows.Next() {
		var md string
		if err := rows.Scan(&md); err != nil {
			return nil, err
		}
		out = append(out, md)
	}
	return out, rows.Err()
}

// ProvisionMissingMailboxes creates the mailbox of every account that has a
// mailbox path but no mailbox on disk, the state an account created before
// account creation built its mailbox is left in. It returns the paths it
// created; an existing mailbox is not touched.
//
// It creates a mailbox only under one of roots that exists: a path outside them,
// or under a root that is not present (a data volume this process does not
// have mounted), is returned in skipped instead, so the repair never writes a
// mailbox somewhere no daemon reads.
func (d *SQLDirectory) ProvisionMissingMailboxes(roots []string) (created, skipped []string, err error) {
	dirs, err := d.AllMaildirs()
	if err != nil {
		return nil, nil, err
	}
	for _, md := range dirs {
		if !underPresentRoot(md, roots) {
			skipped = append(skipped, md)
			continue
		}
		made, err := provisionIfMissing(md)
		if err != nil {
			return created, skipped, err
		}
		if made {
			created = append(created, md)
		}
	}
	return created, skipped, nil
}

// underPresentRoot reports whether the root path belongs to exists on disk. A
// path belongs to the most specific root that contains it, so a partition
// mounted inside DataDir is not stood in for by DataDir when it is absent.
func underPresentRoot(path string, roots []string) bool {
	owner := ""
	for _, root := range roots {
		if insideRoot(path, root) && len(root) > len(owner) {
			owner = root
		}
	}
	if owner == "" {
		return false
	}
	info, err := os.Stat(owner)
	return err == nil && info.IsDir()
}

// insideRoot reports whether path lies strictly below root.
func insideRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return root != "" && err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// provisionIfMissing creates the mailbox at md when none exists there, reporting
// whether it did.
func provisionIfMissing(md string) (bool, error) {
	st, err := objectstore.OpenExisting(md)
	switch {
	case err == nil:
		return false, st.Close()
	case errors.Is(err, objectstore.ErrNotProvisioned):
		if err := provisionMailbox(md); err != nil {
			return false, fmt.Errorf("directory: provision %s: %w", md, err)
		}
		return true, nil
	default:
		return false, fmt.Errorf("directory: open %s: %w", md, err)
	}
}

// SweepRecoverableItems is the retention sweep: it reads the operator-tunable
// retention window (defaulting to DefaultRecoverableRetentionDays when unset) and
// permanently purges every mailbox's soft-deleted items older than that window,
// returning how many it purged. A window of 0 or less disables auto-purge (items are
// kept until manually purged). A mailbox that cannot be opened is skipped, not fatal.
func (d *SQLDirectory) SweepRecoverableItems(now time.Time) (int, error) {
	s, found, err := d.GetRecoverableSettings()
	if err != nil {
		return 0, err
	}
	days := DefaultRecoverableRetentionDays
	if found {
		days = s.RetentionDays
	}
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	maildirs, err := d.AllMaildirs()
	if err != nil {
		return 0, err
	}
	purged := 0
	var errs []error
	for _, md := range maildirs {
		n, err := purgeMailbox(md, cutoff)
		if err != nil {
			errs = append(errs, err)
		}
		purged += n
	}
	return purged, errors.Join(errs...)
}

// purgeMailbox purges one mailbox's soft-deleted items older than cutoff. A
// mailbox that does not exist has nothing to purge and is never created here;
// any other failure is returned so one bad mailbox does not stop the sweep but is
// still reported.
func purgeMailbox(md string, cutoff time.Time) (int, error) {
	st, err := objectstore.OpenExisting(md)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("directory: open %s: %w", md, err)
	}
	n, err := st.PurgeSoftDeletedOlderThan(cutoff)
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("directory: purge %s: %w", md, err)
	}
	return n, nil
}
