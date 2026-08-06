package directory

import (
	"database/sql"
	"errors"
	"strings"
)

// AdminSession is one server-side record of an administration-panel login. The
// panel token is a self-signed JWT, which cannot be revoked on its own; the token
// carries a jti that keys a row here, so deleting the row ends that session on its
// next request.
type AdminSession struct {
	Jti       string
	Login     string
	CreatedAt int64
	ExpiresAt int64
}

// CreateAdminSession records a new panel login. Login is stored lowercased so a
// later revoke matches regardless of the sign-in's case.
func (d *SQLDirectory) CreateAdminSession(s AdminSession) error {
	if s.Jti == "" || s.Login == "" {
		return errors.New("admin session needs a jti and login")
	}
	_, err := d.db.Exec(
		`INSERT INTO admin_sessions (jti, login, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		s.Jti, strings.ToLower(s.Login), s.CreatedAt, s.ExpiresAt)
	return err
}

// AdminSessionActive reports whether the session jti exists and has not expired at
// now. Revocation deletes the row, so an absent row is revoked-or-expired. Keyed by
// jti alone, since it runs on every panel request.
func (d *SQLDirectory) AdminSessionActive(jti string, now int64) (bool, error) {
	var expires int64
	err := d.db.QueryRow(`SELECT expires_at FROM admin_sessions WHERE jti = ?`, jti).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expires > now, nil
}

// DeleteAdminSession revokes one panel session by jti, scoped to the login so a
// known jti cannot be used to sign another operator out.
func (d *SQLDirectory) DeleteAdminSession(login, jti string) error {
	_, err := d.db.Exec(`DELETE FROM admin_sessions WHERE login = ? AND jti = ?`,
		strings.ToLower(login), jti)
	return err
}

// DeleteAdminSessionsFor revokes every panel session an account holds. It is what a
// password change calls: the old credential must not keep a signed-in browser
// working, wherever that browser is.
func (d *SQLDirectory) DeleteAdminSessionsFor(login string) error {
	_, err := d.db.Exec(`DELETE FROM admin_sessions WHERE login = ?`, strings.ToLower(login))
	return err
}
