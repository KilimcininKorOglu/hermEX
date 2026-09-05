package directory

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"hermex/internal/totp"
)

// TOTPEnrollment is one account's second-factor state. Secret is the shared
// secret an authenticator app holds; Enabled is false while the enrollment is
// still pending, so an abandoned one never gates a login.
type TOTPEnrollment struct {
	Secret  string
	Enabled bool
}

// SecondFactorStore is the optional directory capability the login paths reach
// for. A directory that does not implement it has no second factor at all, so
// the caller must treat its absence as "no account is enrolled" rather than as
// an error, exactly as the other optional capabilities do.
type SecondFactorStore interface {
	TOTPEnrollment(user string) (TOTPEnrollment, bool, error)
	BeginTOTPEnrollment(user, secret string) error
	ActivateTOTP(user string, step int64, recoveryHashes []string) error
	DisableTOTP(user string) error
	ConsumeTOTPStep(user string, step int64) (bool, error)
	ConsumeRecoveryCode(user, code string) (bool, error)
	RecoveryCodesRemaining(user string) (int, error)
}

// TOTPSkew is how many time steps either side of now a code may come from,
// which absorbs the clock drift of the user's phone. One step is thirty seconds.
const TOTPSkew = 1

// SecondFactorEnabled reports whether the account must clear a second factor. A
// directory without the capability has no second factor at all, so it reports
// false; a directory that HAS the capability but cannot answer returns the
// error, because the permissive answer here is the one that skips the factor.
func SecondFactorEnabled(dir any, user string) (bool, error) {
	sf, ok := dir.(SecondFactorStore)
	if !ok {
		return false, nil
	}
	e, found, err := sf.TOTPEnrollment(user)
	if err != nil {
		return false, err
	}
	return found && e.Enabled, nil
}

// SpendSecondFactor accepts a code from the authenticator or one of the recovery
// codes and reports whether one was spent. Both login surfaces call it, so the
// order cannot drift between them: the authenticator is tried first, and a typo
// there never burns a recovery code.
func SpendSecondFactor(dir any, user, code string, now time.Time) (bool, error) {
	sf, ok := dir.(SecondFactorStore)
	if !ok {
		return false, nil
	}
	e, found, err := sf.TOTPEnrollment(user)
	if err != nil || !found || !e.Enabled {
		return false, err
	}
	code = strings.TrimSpace(code)
	if step, matched := totp.Verify(e.Secret, code, now, TOTPSkew); matched {
		// Verify only says the code belongs to this secret and window. The store
		// decides whether that step has already been spent, which is what stops a
		// code observed once from being replayed inside its own window.
		return sf.ConsumeTOTPStep(user, step)
	}
	return sf.ConsumeRecoveryCode(user, code)
}

// userIDFor resolves a username to its row id, reporting ok=false for an unknown
// user rather than an error, because every caller here treats an unknown user
// and an unenrolled one the same way.
func (d *SQLDirectory) userIDFor(username string) (int64, bool, error) {
	var id int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`,
		strings.ToLower(strings.TrimSpace(username))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// TOTPEnrollment returns the account's second-factor state. ok is false when the
// account has never started an enrollment.
func (d *SQLDirectory) TOTPEnrollment(user string) (TOTPEnrollment, bool, error) {
	id, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return TOTPEnrollment{}, false, err
	}
	var e TOTPEnrollment
	var enabled int
	err = d.db.QueryRow(`SELECT secret, enabled FROM user_totp WHERE user_id = ?`, id).
		Scan(&e.Secret, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return TOTPEnrollment{}, false, nil
	}
	if err != nil {
		return TOTPEnrollment{}, false, err
	}
	e.Enabled = enabled != 0
	return e, true, nil
}

// BeginTOTPEnrollment records a fresh, not yet active secret, replacing any
// pending one. It refuses to overwrite an ACTIVE enrollment, because that would
// let anyone holding the session silently swap the second factor for one of
// their own; disabling it first is a separate, deliberate step.
func (d *SQLDirectory) BeginTOTPEnrollment(user, secret string) error {
	id, ok, err := d.userIDFor(user)
	if err != nil {
		return err
	}
	if !ok {
		return sql.ErrNoRows
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The row is locked before it is read, so two enrollments started at once
	// cannot both pass the enabled check and leave the account holding a secret
	// neither user was shown.
	var enabled int
	err = tx.QueryRow(`SELECT enabled FROM user_totp WHERE user_id = ? FOR UPDATE`, id).Scan(&enabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(
			`INSERT INTO user_totp (user_id, secret, enabled, last_step, created_at) VALUES (?, ?, 0, 0, ?)`,
			id, secret, time.Now().Unix()); err != nil {
			return err
		}
	case err != nil:
		return err
	case enabled != 0:
		return ErrTOTPAlreadyEnabled
	default:
		if _, err := tx.Exec(
			`UPDATE user_totp SET secret = ?, last_step = 0, created_at = ? WHERE user_id = ?`,
			secret, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ErrTOTPAlreadyEnabled reports an attempt to replace an active enrollment.
var ErrTOTPAlreadyEnabled = errors.New("directory: the second factor is already enabled")

// ActivateTOTP turns the pending enrollment on and stores its recovery codes,
// in one transaction so an account can never end up enrolled with no way back
// in. step is the code that proved the enrollment, recorded so it cannot be
// replayed as the first login.
func (d *SQLDirectory) ActivateTOTP(user string, step int64, recoveryHashes []string) error {
	id, ok, err := d.userIDFor(user)
	if err != nil {
		return err
	}
	if !ok {
		return sql.ErrNoRows
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE user_totp SET enabled = 1, last_step = ? WHERE user_id = ?`, step, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM user_totp_recovery WHERE user_id = ?`, id); err != nil {
		return err
	}
	for _, h := range recoveryHashes {
		if _, err := tx.Exec(
			`INSERT INTO user_totp_recovery (user_id, code_hash, used_at) VALUES (?, ?, 0)`,
			id, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DisableTOTP removes the enrollment and every recovery code. The codes go with
// it because they are useless without it and would otherwise be reused by a
// later enrollment that never showed them to the user.
func (d *SQLDirectory) DisableTOTP(user string) error {
	id, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM user_totp_recovery WHERE user_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_totp WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ConsumeTOTPStep records a verified code's time step and reports whether it was
// accepted. It is the replay guard, and it is a single conditional UPDATE on
// purpose: a code is valid for its whole step, so two logins presenting the same
// observed code race here, and only the one that moves last_step forward wins.
// A read-then-write in Go would let both through.
func (d *SQLDirectory) ConsumeTOTPStep(user string, step int64) (bool, error) {
	id, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return false, err
	}
	res, err := d.db.Exec(
		`UPDATE user_totp SET last_step = ? WHERE user_id = ? AND enabled = 1 AND last_step < ?`,
		step, id, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// unusedRecoveryCodes reads the account's unspent codes as parallel slices of
// row id and stored hash, so the match runs over every candidate rather than
// asking the database to find one, which would compare in the database's own
// time.
func (d *SQLDirectory) unusedRecoveryCodes(userID int64) ([]int64, []string, error) {
	rows, err := d.db.Query(
		`SELECT id, code_hash FROM user_totp_recovery WHERE user_id = ? AND used_at = 0`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var hashes []string
	for rows.Next() {
		var id int64
		var h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		hashes = append(hashes, h)
	}
	return ids, hashes, rows.Err()
}

// ConsumeRecoveryCode marks a matching unused recovery code as used and reports
// whether one matched. The final UPDATE carries the used_at = 0 condition again,
// so two logins presenting the same code cannot both spend it.
func (d *SQLDirectory) ConsumeRecoveryCode(user, code string) (bool, error) {
	id, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return false, err
	}
	ids, hashes, err := d.unusedRecoveryCodes(id)
	if err != nil {
		return false, err
	}
	i := totp.MatchRecoveryCode(hashes, code)
	if i < 0 {
		return false, nil
	}
	res, err := d.db.Exec(
		`UPDATE user_totp_recovery SET used_at = ? WHERE id = ? AND used_at = 0`,
		time.Now().Unix(), ids[i])
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecoveryCodesRemaining counts the codes the account has left, so the user can
// be told to mint new ones before running out.
func (d *SQLDirectory) RecoveryCodesRemaining(user string) (int, error) {
	id, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.db.QueryRow(
		`SELECT COUNT(*) FROM user_totp_recovery WHERE user_id = ? AND used_at = 0`, id).Scan(&n)
	return n, err
}
