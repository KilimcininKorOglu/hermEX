package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

// privilege_bits: the service privileges a user holds.
const (
	privIMAPPOP3 = 1 << 0
	privSMTP     = 1 << 1
)

// SQLDirectory is a MariaDB/MySQL-backed account directory (the internal spec):
// it resolves an address over username/altname/alias,
// gates on account status, domain status, and object class, and verifies
// crypt(3) sha512 passwords. It implements both Accounts and Authenticator.
type SQLDirectory struct {
	db *sql.DB
}

// NewSQL wraps an open database handle. A user's mailbox store is the object
// store rooted at the maildir directory.
func NewSQL(db *sql.DB) *SQLDirectory {
	return &SQLDirectory{db: db}
}

// EnsureSchema applies the directory DDL idempotently.
func (d *SQLDirectory) EnsureSchema() error {
	for _, stmt := range directoryDDL {
		if _, err := d.db.Exec(stmt); err != nil {
			return fmt.Errorf("directory: apply schema: %w", err)
		}
	}
	return nil
}

// storePath returns the object store directory for a maildir. The store is
// rooted at the maildir itself (its databases and content directories live
// inside).
func (d *SQLDirectory) storePath(maildir string) string {
	return maildir
}

// loginRow is one resolved candidate from the username/altname/alias union.
type loginRow struct {
	password     string
	maildir      string
	addrStatus   int
	displayType  int
	domainStatus int
}

// resolve runs the three-key resolution: the input must match
// exactly one of users.username, altnames.altname, or aliases.aliasname. Zero
// rows (no such address) and more than one (ambiguous) are both a non-match.
func (d *SQLDirectory) resolve(addr string) (loginRow, bool, error) {
	const q = `
SELECT u.password, u.maildir, u.address_status, u.display_type, d.domain_status
  FROM users u JOIN domains d ON u.domain_id = d.id
 WHERE u.username = ?
UNION
SELECT u.password, u.maildir, u.address_status, u.display_type, d.domain_status
  FROM users u JOIN domains d ON u.domain_id = d.id
  JOIN altnames a ON a.user_id = u.id
 WHERE a.altname = ?
UNION
SELECT u.password, u.maildir, u.address_status, u.display_type, d.domain_status
  FROM users u JOIN domains d ON u.domain_id = d.id
  JOIN aliases al ON al.mainname = u.username
 WHERE al.aliasname = ?
 LIMIT 2`
	rows, err := d.db.Query(q, addr, addr, addr)
	if err != nil {
		return loginRow{}, false, err
	}
	defer rows.Close()
	var out []loginRow
	for rows.Next() {
		var r loginRow
		if err := rows.Scan(&r.password, &r.maildir, &r.addrStatus, &r.displayType, &r.domainStatus); err != nil {
			return loginRow{}, false, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return loginRow{}, false, err
	}
	if len(out) != 1 {
		return loginRow{}, false, nil
	}
	return out[0], true, nil
}

// Authenticate verifies a login and returns the user's mailbox store path:
// resolve to exactly one row, require an active
// MAILUSER account in an active domain, then verify the crypt(3) password.
func (d *SQLDirectory) Authenticate(user, password string) (string, bool) {
	row, ok, err := d.resolve(strings.ToLower(strings.TrimSpace(user)))
	if err != nil || !ok {
		return "", false
	}
	if row.displayType != dtMailuser {
		return "", false
	}
	// Only AF_USER_NORMAL in an active domain may log in.
	if row.addrStatus&afUserMask != afUserNormal || row.addrStatus&afDomainMask != 0 || row.domainStatus != 0 {
		return "", false
	}
	if row.maildir == "" || !sqlCryptVerify(password, row.password) {
		return "", false
	}
	return d.storePath(row.maildir), true
}

// Resolve maps a recipient address to the store path it is delivered to,
// accepting it only when the account can receive (NORMAL or shared mailbox) and
// its domain is active. Unknown addresses refuse.
func (d *SQLDirectory) Resolve(address string) (string, bool) {
	row, ok, err := d.resolve(strings.ToLower(strings.TrimSpace(address)))
	if err != nil || !ok {
		return "", false
	}
	if row.domainStatus != 0 || row.maildir == "" {
		return "", false
	}
	u := row.addrStatus & afUserMask
	if (u != afUserNormal && u != afUserSharedMbox) || row.addrStatus&afDomainMask != 0 {
		return "", false
	}
	return d.storePath(row.maildir), true
}

// Identities implements Identifier: the addresses login may send as — its
// canonical username plus every alias (aliases.mainname) and altname
// (altnames.user_id) bound to that user. login may itself be a username, alias,
// or altname; an unknown login yields no identities (the webmail then permits
// send-as-self only).
func (d *SQLDirectory) Identities(login string) ([]string, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	var id int64
	var username string
	err := d.db.QueryRow(`
SELECT u.id, u.username FROM users u WHERE u.username = ?
UNION
SELECT u.id, u.username FROM users u JOIN aliases al ON al.mainname = u.username WHERE al.aliasname = ?
UNION
SELECT u.id, u.username FROM users u JOIN altnames a ON a.user_id = u.id WHERE a.altname = ?
LIMIT 1`, login, login, login).Scan(&id, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{username}
	rows, err := d.db.Query(`
SELECT aliasname FROM aliases WHERE mainname = ?
UNION
SELECT altname FROM altnames WHERE user_id = ?`, username, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Maildirs implements MailboxLister: the distinct store paths of every
// login-capable user mailbox — a normal MAILUSER account in an active domain
// with a maildir. These are exactly the accounts that can schedule a send, so
// the send-later spooler scans their Outboxes; disabled accounts and non-mailbox
// objects (distribution lists, rooms) are skipped.
func (d *SQLDirectory) Maildirs() ([]string, error) {
	const q = `
SELECT DISTINCT u.maildir
  FROM users u JOIN domains d ON u.domain_id = d.id
 WHERE u.maildir <> ''
   AND u.display_type = ?
   AND (u.address_status & ?) = ?
   AND (u.address_status & ?) = 0
   AND d.domain_status = 0`
	rows, err := d.db.Query(q, dtMailuser, afUserMask, afUserNormal, afDomainMask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var maildir string
		if err := rows.Scan(&maildir); err != nil {
			return nil, err
		}
		out = append(out, d.storePath(maildir))
	}
	return out, rows.Err()
}

// CreateDomain inserts a domain and returns its id, creating its homedir on disk.
func (d *SQLDirectory) CreateDomain(domainname, homedir string) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO domains (domainname, homedir, org_id, homeserver, domain_status)
		 VALUES (?, ?, 0, 0, 0)`,
		strings.ToLower(domainname), homedir)
	if err != nil {
		return 0, err
	}
	if homedir != "" {
		if err := os.MkdirAll(homedir, 0o700); err != nil {
			return 0, err
		}
	}
	return res.LastInsertId()
}

// CreateUser inserts a mailbox user (username is its e-mail address) with a
// freshly crypt(3)-hashed password and the given maildir, creating the maildir
// on disk. The user's domain must already exist.
func (d *SQLDirectory) CreateUser(username, password, maildir string) (int64, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	at := strings.LastIndexByte(username, '@')
	if at <= 0 {
		return 0, errors.New("directory: username must be an email address")
	}
	domain := username[at+1:]
	var domainID int64
	err := d.db.QueryRow(`SELECT id FROM domains WHERE domainname = ?`, domain).Scan(&domainID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("directory: domain %q not found", domain)
	}
	if err != nil {
		return 0, err
	}
	hash, err := sqlCryptNewHash(password)
	if err != nil {
		return 0, err
	}
	res, err := d.db.Exec(
		`INSERT INTO users
		   (username, password, domain_id, homeserver, maildir, lang, timezone, privilege_bits, address_status, display_type)
		 VALUES (?, ?, ?, 0, ?, '', '', ?, 0, 0)`,
		username, hash, domainID, maildir, privIMAPPOP3|privSMTP)
	if err != nil {
		return 0, err
	}
	if maildir != "" {
		if err := os.MkdirAll(maildir, 0o700); err != nil {
			return 0, err
		}
	}
	return res.LastInsertId()
}

// CreateAlias maps an alternate address (aliasname) to a canonical user
// (mainname == users.username) in the aliases table.
func (d *SQLDirectory) CreateAlias(aliasname, mainname string) error {
	_, err := d.db.Exec(`INSERT INTO aliases (aliasname, mainname) VALUES (?, ?)`,
		strings.ToLower(strings.TrimSpace(aliasname)), strings.ToLower(strings.TrimSpace(mainname)))
	return err
}

// sqlCryptNewHash produces a sha512-crypt ($6$) hash with a random salt, the
// default credential scheme for the directory.
func sqlCryptNewHash(password string) (string, error) {
	return sha512_crypt.New().Generate([]byte(password), nil)
}

// sqlCryptVerify checks a password against a stored sha512-crypt hash. An empty
// stored hash never matches.
func sqlCryptVerify(password, stored string) bool {
	if stored == "" {
		return false
	}
	return sha512_crypt.New().Verify(stored, []byte(password)) == nil
}
