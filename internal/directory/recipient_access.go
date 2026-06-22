package directory

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RecipientRule is one mailbox owner's personal allow/block rule. Pattern is a
// lowercased email address or bare domain; Action is SenderAllow or SenderBlock.
type RecipientRule struct {
	Pattern   string
	Action    string
	CreatedAt int64
}

// RecipientRulesForMaildir returns the personal allow/block rules of the mailbox at
// maildir as a pattern→action map — the exact shape antispam.NewAccessList consumes —
// so the MTA resolves a per-recipient verdict with the same matching as the operator's
// server-wide list, with no risk of the two drifting. The map is empty when the user
// has no rules or is unknown; the maildir identifies the user, so an alias recipient
// resolves to the owner's rules. The caller treats an error as no rules (fail-open).
func (d *SQLDirectory) RecipientRulesForMaildir(maildir string) (map[string]string, error) {
	rows, err := d.db.Query(
		`SELECT ra.pattern, ra.action
		   FROM recipient_access ra JOIN users u ON ra.user_id = u.id
		  WHERE u.maildir = ?`, maildir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var pattern, action string
		if err := rows.Scan(&pattern, &action); err != nil {
			return nil, err
		}
		out[pattern] = action
	}
	return out, rows.Err()
}

// SetRecipientRule upserts a user's personal allow/block rule for a pattern (a
// lowercased email address or bare domain), flipping the action when the pattern
// already exists so it carries exactly one action. It rejects an empty pattern, an
// unknown action, or an unknown user.
func (d *SQLDirectory) SetRecipientRule(username, pattern, action string) error {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return errors.New("recipient rule pattern is empty")
	}
	if action != SenderAllow && action != SenderBlock {
		return errors.New("recipient rule action must be allow or block")
	}
	username = strings.ToLower(strings.TrimSpace(username))
	var userID int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("no such user")
	}
	if err != nil {
		return err
	}
	_, err = d.db.Exec(
		`INSERT INTO recipient_access (user_id, pattern, action, created_at) VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE action = VALUES(action)`,
		userID, pattern, action, time.Now().Unix())
	return err
}

// DeleteRecipientRule removes a user's personal rule by pattern; ok is false when no
// rule matched.
func (d *SQLDirectory) DeleteRecipientRule(username, pattern string) (bool, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	username = strings.ToLower(strings.TrimSpace(username))
	res, err := d.db.Exec(
		`DELETE ra FROM recipient_access ra JOIN users u ON ra.user_id = u.id
		  WHERE u.username = ? AND ra.pattern = ?`, username, pattern)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListRecipientRules returns a user's personal rules, blocks first then allows,
// alphabetical within each, for the management UI.
func (d *SQLDirectory) ListRecipientRules(username string) ([]RecipientRule, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	rows, err := d.db.Query(
		`SELECT ra.pattern, ra.action, ra.created_at
		   FROM recipient_access ra JOIN users u ON ra.user_id = u.id
		  WHERE u.username = ? ORDER BY ra.action, ra.pattern`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecipientRule
	for rows.Next() {
		var r RecipientRule
		if err := rows.Scan(&r.Pattern, &r.Action, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
