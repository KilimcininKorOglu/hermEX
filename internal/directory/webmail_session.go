package directory

import (
	"database/sql"
	"errors"
	"strings"
)

// WebmailSession is one server-side record of a webmail2 login, so a user can list
// their active sessions and revoke one - the stateless JWT alone cannot be listed or
// revoked. Jti keys the row and is carried in the token; deleting the row revokes
// that session on its next request.
type WebmailSession struct {
	Jti        string
	Email      string
	DeviceType string
	UserAgent  string
	ClientIP   string
	CreatedAt  int64
	LastActive int64
	ExpiresAt  int64
}

// CreateWebmailSession records a new login session. Email is stored lowercased so
// list/revoke match regardless of the login's case.
func (d *SQLDirectory) CreateWebmailSession(s WebmailSession) error {
	if s.Jti == "" || s.Email == "" {
		return errors.New("webmail session needs a jti and email")
	}
	_, err := d.db.Exec(
		`INSERT INTO webmail_sessions
		   (jti, email, device_type, user_agent, client_ip, created_at, last_active, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Jti, strings.ToLower(s.Email), s.DeviceType, s.UserAgent, s.ClientIP,
		s.CreatedAt, s.LastActive, s.ExpiresAt)
	return err
}

// WebmailSessionActive reports whether the session jti exists and has not expired at
// now. Revocation deletes the row, so an absent row is revoked-or-expired. Keyed by
// jti alone (the per-request hot path), so it needs no email.
func (d *SQLDirectory) WebmailSessionActive(jti string, now int64) (bool, error) {
	var expires int64
	err := d.db.QueryRow(`SELECT expires_at FROM webmail_sessions WHERE jti = ?`, jti).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expires > now, nil
}

// TouchWebmailSession refreshes a session's last-active time, but only when the
// stored value is more than 30s stale, so a busy session is not written on every
// request.
func (d *SQLDirectory) TouchWebmailSession(jti string, now int64) error {
	_, err := d.db.Exec(
		`UPDATE webmail_sessions SET last_active = ? WHERE jti = ? AND last_active < ?`,
		now, jti, now-30)
	return err
}

// ListWebmailSessions returns a user's non-expired sessions, most-recently-active
// first, for the security UI.
func (d *SQLDirectory) ListWebmailSessions(email string, now int64) ([]WebmailSession, error) {
	rows, err := d.db.Query(
		`SELECT jti, email, device_type, user_agent, client_ip, created_at, last_active, expires_at
		   FROM webmail_sessions WHERE email = ? AND expires_at > ? ORDER BY last_active DESC`,
		strings.ToLower(email), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebmailSession
	for rows.Next() {
		var s WebmailSession
		if err := rows.Scan(&s.Jti, &s.Email, &s.DeviceType, &s.UserAgent, &s.ClientIP,
			&s.CreatedAt, &s.LastActive, &s.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteWebmailSession revokes a user's session by jti, scoped to email so a user can
// only revoke their OWN session; ok is false when nothing matched.
func (d *SQLDirectory) DeleteWebmailSession(email, jti string) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM webmail_sessions WHERE email = ? AND jti = ?`,
		strings.ToLower(email), jti)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
