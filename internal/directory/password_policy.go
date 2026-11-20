package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// DefaultMinPasswordLength is the minimum in force until an operator saves one.
const DefaultMinPasswordLength = 8

// MaxMinPasswordLength bounds the setting, so a typo cannot make every new
// password impossible to set.
const MaxMinPasswordLength = 128

// ErrPasswordTooShort reports a new password below the configured minimum. A
// caller answers it as the client's error; PasswordTooShortError carries the
// minimum for the message.
var ErrPasswordTooShort = errors.New("directory: password too short")

// PasswordTooShortError is the error a refused password returns.
type PasswordTooShortError struct{ Min int }

func (e *PasswordTooShortError) Error() string {
	return fmt.Sprintf("directory: the password must be at least %d characters", e.Min)
}

// Is makes errors.Is(err, ErrPasswordTooShort) match.
func (e *PasswordTooShortError) Is(target error) bool { return target == ErrPasswordTooShort }

// MinPasswordLength returns the minimum from err when it is a too-short refusal.
func MinPasswordLength(err error) (int, bool) {
	if e, ok := errors.AsType[*PasswordTooShortError](err); ok {
		return e.Min, true
	}
	return 0, false
}

// GetMinPasswordLength returns the stored minimum and whether a row was saved.
func (d *SQLDirectory) GetMinPasswordLength() (int, bool, error) {
	var n int
	err := d.db.QueryRow(`SELECT min_length FROM password_policy_settings WHERE id = 1`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultMinPasswordLength, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// SetMinPasswordLength stores the minimum, refusing a value outside
// 1..MaxMinPasswordLength.
func (d *SQLDirectory) SetMinPasswordLength(n int) error {
	if n < 1 || n > MaxMinPasswordLength {
		return fmt.Errorf("directory: the minimum password length must be 1..%d", MaxMinPasswordLength)
	}
	_, err := d.db.Exec(
		`INSERT INTO password_policy_settings (id, min_length, updated_at) VALUES (1, ?, ?)
		 ON DUPLICATE KEY UPDATE min_length = VALUES(min_length), updated_at = VALUES(updated_at)`,
		n, time.Now().UnixMilli())
	return err
}

// checkNewPassword refuses a password below the stored minimum, counted in
// characters. A minimum that cannot be read refuses the password rather than
// admitting it with no rule.
func (d *SQLDirectory) checkNewPassword(password string) error {
	minLen, _, err := d.GetMinPasswordLength()
	if err != nil {
		return fmt.Errorf("directory: read the password policy: %w", err)
	}
	if utf8.RuneCountInString(password) < minLen {
		return &PasswordTooShortError{Min: minLen}
	}
	return nil
}
