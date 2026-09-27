package directory

import (
	"errors"
	"net/url"
	"strings"
)

// HealthTarget is one daemon the admin Live status page probes: a display name
// and the URL of the daemon's /healthz endpoint.
type HealthTarget struct {
	ID   int64
	Name string
	URL  string
}

// maxHealthTargetName and maxHealthTargetURL are the widths of the table's columns.
const (
	maxHealthTargetName = 64
	maxHealthTargetURL  = 512
)

var (
	// ErrInvalidHealthTarget reports a name or URL the monitor could not use.
	ErrInvalidHealthTarget = errors.New("directory: invalid health target")
	// ErrHealthTargetExists reports a name another target already uses.
	ErrHealthTargetExists = errors.New("directory: a health target with this name exists")
)

// validHealthTarget checks that a target has a name and an absolute http or
// https URL with a host, each within its column.
func validHealthTarget(t HealthTarget) bool {
	if t.Name == "" || len(t.Name) > maxHealthTargetName || len(t.URL) > maxHealthTargetURL {
		return false
	}
	u, err := url.Parse(t.URL)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// ListHealthTargets returns every stored target in name order. The status page
// reads it on every probe, so an added or removed target shows at once.
func (d *SQLDirectory) ListHealthTargets() ([]HealthTarget, error) {
	rows, err := d.db.Query(`SELECT id, name, url FROM health_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HealthTarget
	for rows.Next() {
		var t HealthTarget
		if err := rows.Scan(&t.ID, &t.Name, &t.URL); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddHealthTarget stores a target and returns its id. It refuses an unusable
// name or URL with ErrInvalidHealthTarget and a taken name with
// ErrHealthTargetExists.
func (d *SQLDirectory) AddHealthTarget(t HealthTarget) (int64, error) {
	t.Name, t.URL = strings.TrimSpace(t.Name), strings.TrimSpace(t.URL)
	if !validHealthTarget(t) {
		return 0, ErrInvalidHealthTarget
	}
	res, err := d.db.Exec(`INSERT INTO health_targets (name, url) VALUES (?, ?)`, t.Name, t.URL)
	if isDuplicateKey(err) {
		return 0, ErrHealthTargetExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteHealthTarget removes a target, reporting whether a row went.
func (d *SQLDirectory) DeleteHealthTarget(id int64) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM health_targets WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
