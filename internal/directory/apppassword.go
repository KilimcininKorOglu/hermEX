package directory

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// MaxAppPasswords bounds how many credentials one account may hold. Every one of
// them is a way into the mailbox, so an unbounded list is an unbounded attack
// surface the user has stopped reading.
const MaxAppPasswords = 20

// appSecretBytes is one credential's entropy. Eighty bits is past guessing, which
// is what lets the stored form be a plain digest.
const appSecretBytes = 10

// ErrTooManyAppPasswords reports that the account already holds the maximum.
var ErrTooManyAppPasswords = errors.New("directory: too many app passwords")

// appEncoding drops the padding and reads back case-insensitively, so the user
// may type the credential as they were shown it or as their client lowercases it.
var appEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// AppPassword is one stored credential as the user sees it. The secret itself is
// never returned after it is minted, because only its hash is kept.
type AppPassword struct {
	ID         int64
	Name       string
	CreatedAt  int64
	LastUsedAt int64
}

// AppPasswordStore is the optional directory capability the protocol logins and
// the webmail settings reach for. A directory without it has no app passwords,
// so a protocol login falls back to the account password exactly as before.
type AppPasswordStore interface {
	CreateAppPassword(user, name string) (secret string, err error)
	ListAppPasswords(user string) ([]AppPassword, error)
	DeleteAppPassword(user string, id int64) (bool, error)
	AuthenticateAppPassword(user, password string) (maildir string, ok bool)
}

// hashAppSecret returns the stored form. Normalisation happens here and nowhere
// else, so a secret hashes the same at mint time and at every login.
func hashAppSecret(secret string) string {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:])
}

// CreateAppPassword mints a credential for one mail program and returns the
// secret, which is shown to the user once and never stored.
func (d *SQLDirectory) CreateAppPassword(user, name string) (string, error) {
	id, ok, err := d.userIDFor(user)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", sql.ErrNoRows
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Mail client"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM app_passwords WHERE user_id = ?`, id).Scan(&n); err != nil {
		return "", err
	}
	if n >= MaxAppPasswords {
		return "", ErrTooManyAppPasswords
	}
	b := make([]byte, appSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := appEncoding.EncodeToString(b)
	if _, err := d.db.Exec(
		`INSERT INTO app_passwords (user_id, name, secret_hash, created_at, last_used_at) VALUES (?, ?, ?, ?, 0)`,
		id, name, hashAppSecret(secret), time.Now().Unix()); err != nil {
		return "", err
	}
	return secret, nil
}

// ListAppPasswords returns the account's credentials, newest first, without
// their secrets.
func (d *SQLDirectory) ListAppPasswords(user string) ([]AppPassword, error) {
	id, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.db.Query(
		`SELECT id, name, created_at, last_used_at FROM app_passwords WHERE user_id = ? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppPassword
	for rows.Next() {
		var p AppPassword
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteAppPassword revokes one credential. The user id is part of the condition
// rather than checked beforehand, so a caller cannot revoke another account's
// credential by guessing its id.
func (d *SQLDirectory) DeleteAppPassword(user string, id int64) (bool, error) {
	uid, ok, err := d.userIDFor(user)
	if err != nil || !ok {
		return false, err
	}
	res, err := d.db.Exec(`DELETE FROM app_passwords WHERE id = ? AND user_id = ?`, id, uid)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// matchAppPassword returns the id of the account's credential the submitted
// secret matches, or 0. Every stored hash is compared even after a match, so the
// answer's timing does not say which credential was used, or how many exist.
func (d *SQLDirectory) matchAppPassword(userID int64, password string) (int64, error) {
	rows, err := d.db.Query(`SELECT id, secret_hash FROM app_passwords WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	want := hashAppSecret(password)
	var matched int64
	for rows.Next() {
		var id int64
		var h string
		if err := rows.Scan(&id, &h); err != nil {
			return 0, err
		}
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			matched = id
		}
	}
	return matched, rows.Err()
}

// AuthenticateAppPassword verifies a credential and returns the account's
// mailbox path.
func (d *SQLDirectory) AuthenticateAppPassword(user, password string) (string, bool) {
	// The same resolution and eligibility rules the account password goes
	// through: exactly one of username, altname or alias, an active mail user in
	// an active domain, with a mailbox. A credential must not outlive the account
	// being disabled.
	row, found, err := d.resolve(strings.ToLower(strings.TrimSpace(user)))
	if err != nil || !found || !row.eligible() || strings.TrimSpace(password) == "" {
		return "", false
	}
	matched, err := d.matchAppPassword(row.id, password)
	if err != nil || matched == 0 {
		return "", false
	}
	path := d.storePath(row.maildir)
	// Best-effort: the login has already succeeded, so a failed write must not
	// turn it into a failure. It only feeds the "last used" column the user reads
	// when deciding which credential to revoke.
	_, _ = d.db.Exec(`UPDATE app_passwords SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), matched)
	return path, true
}

// AuthenticateProtocol is what every client protocol calls: IMAP, POP3, SMTP
// submission, ActiveSync, EWS, DAV and MAPI. None of them can ask for a second
// factor, so an account that has enrolled one accepts ONLY an app password here.
// An account without a second factor keeps accepting its own password, so
// nothing changes for a user who has not asked for one.
//
// A directory that cannot say whether the account is enrolled denies the login,
// because the permissive answer is the one that admits the account password to a
// protocol the second factor was supposed to close.
func (d *SQLDirectory) AuthenticateProtocol(user, password string) (string, bool) {
	enabled, err := SecondFactorEnabled(d, user)
	if err != nil {
		return "", false
	}
	if !enabled {
		if path, ok := d.Authenticate(user, password); ok {
			return path, true
		}
	}
	return d.AuthenticateAppPassword(user, password)
}

// ClientAuthenticator is the minimum a protocol server holds. AuthenticateClient
// reaches past it for the app-password capability when the directory has one.
type ClientAuthenticator interface {
	Authenticate(user, password string) (maildir string, ok bool)
}

// AuthenticateClient is the single entry point for a client protocol login. It
// lives here rather than in each protocol package so the seven of them cannot
// drift: a directory that supports app passwords routes through
// AuthenticateProtocol, and one that does not keeps the previous behavior.
func AuthenticateClient(auth ClientAuthenticator, user, password string) (string, bool) {
	if p, ok := auth.(interface {
		AuthenticateProtocol(user, password string) (string, bool)
	}); ok {
		return p.AuthenticateProtocol(user, password)
	}
	return auth.Authenticate(user, password)
}
