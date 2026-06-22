package directory

import (
	"errors"
	"strings"
	"time"
)

// Sender access actions: an allowlisted sender is rescued from score-based junking
// (a hard DMARC failure still wins); a blocklisted sender is always filed to Junk.
const (
	SenderAllow = "allow"
	SenderBlock = "block"
)

// SenderRule is one operator-managed allow/block rule. Pattern is a lowercased
// email address or bare domain.
type SenderRule struct {
	ID        int64
	Pattern   string
	Action    string
	CreatedAt int64
}

// SetSenderRule upserts an allow/block rule for a pattern (a lowercased email
// address or bare domain), flipping the action when the pattern already exists so a
// pattern always carries exactly one action. It rejects an empty pattern or an
// unknown action.
func (d *SQLDirectory) SetSenderRule(pattern, action string) error {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return errors.New("sender rule pattern is empty")
	}
	if action != SenderAllow && action != SenderBlock {
		return errors.New("sender rule action must be allow or block")
	}
	_, err := d.db.Exec(
		`INSERT INTO sender_access (pattern, action, created_at) VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE action = VALUES(action)`,
		pattern, action, time.Now().Unix())
	return err
}

// DeleteSenderRule removes a rule by pattern; ok is false when none matched.
func (d *SQLDirectory) DeleteSenderRule(pattern string) (bool, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	res, err := d.db.Exec(`DELETE FROM sender_access WHERE pattern = ?`, pattern)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListSenderRules returns every rule, blocks first then allows, alphabetical within
// each, for the admin page.
func (d *SQLDirectory) ListSenderRules() ([]SenderRule, error) {
	rows, err := d.db.Query(
		`SELECT id, pattern, action, created_at FROM sender_access ORDER BY action, pattern`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SenderRule
	for rows.Next() {
		var r SenderRule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.Action, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
