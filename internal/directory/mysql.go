package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/GehirnInc/crypt/md5_crypt"
	"github.com/GehirnInc/crypt/sha512_crypt"

	"hermex/internal/migrate"
)

// privilege_bits: the service privileges a user holds. The bit positions match
// the reference's wire-shared column, so a value reads the same regardless of who
// wrote it. POP3/IMAP, SMTP and CHGPASSWD are plain bits (set = granted). WEB/EAS/
// DAV use the DETAIL1 opt-out convention: they default to granted and are revoked
// only when DETAIL1 is set AND the service's own bit is clear, so a legacy row
// (DETAIL1 unset) reads every service granted. (Chat/Video/Files/Archive bits
// exist in the reference but address subsystems hermEX does not have, so they are
// not modeled here; UpdateUser preserves them untouched.)
const (
	privIMAPPOP3  = 1 << 0
	privSMTP      = 1 << 1
	privChgPasswd = 1 << 2
	privDetail1   = 1 << 8
	privWeb       = 1 << 9
	privEAS       = 1 << 10
	privDAV       = 1 << 11
)

// SQLDirectory is a MariaDB/MySQL-backed account directory (the internal spec):
// it resolves an address over username/altname/alias,
// gates on account status, domain status, and object class, and verifies
// crypt(3) sha512 passwords. It implements both Accounts and Authenticator.
type SQLDirectory struct {
	db       *sql.DB
	verifier LDAPVerifier // verifies LDAP-mastered (externid) logins; nil => denied
}

// NewSQL wraps an open database handle. A user's mailbox store is the object
// store rooted at the maildir directory.
func NewSQL(db *sql.DB) *SQLDirectory {
	return &SQLDirectory{db: db}
}

// EnsureSchema brings the directory database to the latest schema version,
// applying any pending migrations once and recording each in schema_migrations.
// It is safe to call on every startup: an up-to-date database is left untouched,
// and one recorded newer than this binary is refused rather than downgraded.
func (d *SQLDirectory) EnsureSchema() error {
	if err := migrate.Run(context.Background(), &migrate.MySQLDriver{DB: d.db}, 0, directoryMigrations); err != nil {
		return fmt.Errorf("directory: apply schema: %w", err)
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
	externid     []byte // non-nil => the account is mastered in an LDAP directory
	orgID        int64  // the account's organization (selects its LDAP config)
}

// resolve runs the three-key resolution: the input must match
// exactly one of users.username, altnames.altname, or aliases.aliasname. Zero
// rows (no such address) and more than one (ambiguous) are both a non-match.
func (d *SQLDirectory) resolve(addr string) (loginRow, bool, error) {
	const q = `
SELECT u.password, u.maildir, u.address_status, u.display_type, d.domain_status, u.externid, d.org_id
  FROM users u JOIN domains d ON u.domain_id = d.id
 WHERE u.username = ?
UNION
SELECT u.password, u.maildir, u.address_status, u.display_type, d.domain_status, u.externid, d.org_id
  FROM users u JOIN domains d ON u.domain_id = d.id
  JOIN altnames a ON a.user_id = u.id
 WHERE a.altname = ?
UNION
SELECT u.password, u.maildir, u.address_status, u.display_type, d.domain_status, u.externid, d.org_id
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
		if err := rows.Scan(&r.password, &r.maildir, &r.addrStatus, &r.displayType, &r.domainStatus, &r.externid, &r.orgID); err != nil {
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
	login := strings.ToLower(strings.TrimSpace(user))
	row, ok, err := d.resolve(login)
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
	if row.maildir == "" || !d.verifyPassword(row, login, password) {
		return "", false
	}
	return d.storePath(row.maildir), true
}

// verifyPassword checks a login's password the way its account is mastered: an
// account with an externid is verified against its organization's LDAP directory
// (bind-to-verify), every other account against its stored crypt(3) hash. An
// LDAP-mastered account whose org has no configured directory — or for which no
// verifier is installed — is denied rather than silently falling back to a local
// hash it does not own.
func (d *SQLDirectory) verifyPassword(row loginRow, login, password string) bool {
	if len(row.externid) == 0 {
		return sqlCryptVerify(password, row.password)
	}
	if d.verifier == nil {
		return false
	}
	cfg, ok, err := d.GetLDAPConfig(row.orgID)
	if err != nil || !ok {
		return false
	}
	verified, err := d.verifier.Verify(cfg, login, password)
	return err == nil && verified
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

// IsLocalDomain implements LocalDomains: a domain is local when it exists in the
// domains table and is active (domain_status = 0). The lookup is
// case-insensitive, matching how domain names are stored.
func (d *SQLDirectory) IsLocalDomain(domain string) (bool, error) {
	var one int
	err := d.db.QueryRow(
		`SELECT 1 FROM domains WHERE domainname = ? AND domain_status = 0`,
		strings.ToLower(strings.TrimSpace(domain))).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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

// SearchGAL implements GAL: a case-insensitive substring match over the
// addresses of the address-book objects in an active domain — mailbox users with
// a maildir (DT_MAILUSER) and distribution lists (DT_DISTLIST, which have no
// mailbox) — ordered by address and capped at limit. It returns one entry per
// object: aliases and altnames are deliberately not searched, since inbound alias
// delivery already works via Resolve and folding them in would suggest one person
// several times. DisplayName is the object's PR_DISPLAY_NAME from user_properties,
// falling back to the address when none is set; DisplayType carries the object
// class (the users.display_type column, not joined from user_properties).
func (d *SQLDirectory) SearchGAL(query string, limit int) ([]GALEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	// Escape LIKE metacharacters so a typed % or _ matches literally; the pattern
	// is a bound parameter, so only the ESCAPE clause sits in the SQL text (where
	// '\\' is how MySQL spells a single backslash escape character).
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(strings.TrimSpace(query)))
	const prDisplayName = 0x3001001F    // PR_DISPLAY_NAME (PtUnicode)
	const prAttrHiddenMask = 0x10F40003 // PR_ATTR_HIDDEN, PtLong mask form
	const prAttrHiddenBool = 0x10F4000B // PR_ATTR_HIDDEN, PtBoolean legacy form
	// The hide mask is loaded but NOT filtered here: SearchGAL feeds both GAL
	// enumeration and name resolution, which hide on different bits, so the
	// per-surface filtering lives in the NSPI layer. The SQL only loads the raw
	// mask; the address-book code applies the bit appropriate to each query.
	const q = `
SELECT u.username, u.display_type, dn.propval_str, hg.propval_str, hb.propval_str
  FROM users u JOIN domains d ON u.domain_id = d.id
  LEFT JOIN user_properties dn ON dn.user_id = u.id AND dn.proptag = ? AND dn.order_id = 1
  LEFT JOIN user_properties hg ON hg.user_id = u.id AND hg.proptag = ? AND hg.order_id = 1
  LEFT JOIN user_properties hb ON hb.user_id = u.id AND hb.proptag = ? AND hb.order_id = 1
 WHERE u.display_type IN (?, ?, ?, ?, ?)
   AND (u.maildir <> '' OR u.display_type IN (?, ?))
   AND (u.address_status & ?) = ?
   AND (u.address_status & ?) = 0
   AND d.domain_status = 0
   AND u.username LIKE ? ESCAPE '\\'
 ORDER BY u.username
 LIMIT ?`
	rows, err := d.db.Query(q, prDisplayName, prAttrHiddenMask, prAttrHiddenBool,
		dtMailuser, dtDistlist, dtContact, dtRoom, dtEquipment, // display_type IN
		dtDistlist, dtContact, // maildir-exempt types (no mailbox)
		afUserMask, afUserNormal, afDomainMask, "%"+esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GALEntry
	for rows.Next() {
		var addr string
		var displayType int
		var name, hideMask, hideBool sql.NullString
		if err := rows.Scan(&addr, &displayType, &name, &hideMask, &hideBool); err != nil {
			return nil, err
		}
		display := addr
		if name.Valid && name.String != "" {
			display = name.String
		}
		out = append(out, GALEntry{DisplayName: display, Address: addr, DisplayType: displayType, HiddenFrom: hideMaskFromProps(hideMask, hideBool)})
	}
	return out, rows.Err()
}

// hideMaskFromProps decodes a user's address-book hide mask from its two source
// properties. The PtLong mask form of PR_ATTR_HIDDEN is parsed base-0 (so "0x03"
// and "3" both work) and wins when present; absent that, the legacy boolean form,
// when truthy, means hidden from the GAL and address lists (mask 0x03). An
// unparsable value reads as visible.
func hideMaskFromProps(mask, boolean sql.NullString) uint32 {
	if mask.Valid && mask.String != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(mask.String), 0, 32); err == nil {
			return uint32(v)
		}
	}
	if boolean.Valid {
		if v, err := strconv.ParseUint(strings.TrimSpace(boolean.String), 0, 32); err == nil && v != 0 {
			return 0x03
		}
	}
	return 0
}

// DomainInfo is a domain's administrative summary.
type DomainInfo struct {
	ID     int64
	Name   string
	OrgID  int64
	Status int
}

// ListDomains returns every domain, ordered by name, for the admin API.
func (d *SQLDirectory) ListDomains() ([]DomainInfo, error) {
	rows, err := d.db.Query(`SELECT id, domainname, org_id, domain_status FROM domains ORDER BY domainname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainInfo
	for rows.Next() {
		var di DomainInfo
		if err := rows.Scan(&di.ID, &di.Name, &di.OrgID, &di.Status); err != nil {
			return nil, err
		}
		out = append(out, di)
	}
	return out, rows.Err()
}

// UserInfo is a user's administrative summary. LDAP is true when the account is
// LDAP-mastered (its externid is set), so it authenticates against LDAP rather
// than the local hash.
type UserInfo struct {
	ID       int64
	Username string
	DomainID int64
	Status   int
	LDAP     bool
}

// ListUsers returns every user, ordered by name, for the admin API.
func (d *SQLDirectory) ListUsers() ([]UserInfo, error) {
	rows, err := d.db.Query(
		`SELECT id, username, domain_id, address_status, externid IS NOT NULL FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserInfo
	for rows.Next() {
		var ui UserInfo
		var ldap int
		if err := rows.Scan(&ui.ID, &ui.Username, &ui.DomainID, &ui.Status, &ldap); err != nil {
			return nil, err
		}
		ui.LDAP = ldap != 0
		out = append(out, ui)
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

// DomainDetail is a domain's full administrative record for the detail/edit view.
// Status is domain_status (0 = active, 1 = suspended); MaxUser caps the mailbox
// count (0 = unlimited). The three user counts are derived from the domain's
// users by their address_status nibble and maildir, matching the reference's
// active/inactive/virtual split.
type DomainDetail struct {
	ID            int64
	Name          string
	OrgID         int64
	Status        int
	Homedir       string
	MaxUser       int64
	Title         string
	Address       string
	AdminName     string
	Tel           string
	ActiveUsers   int64
	InactiveUsers int64
	VirtualUsers  int64
}

// DomainUpdate carries the editable fields of a domain. Status is domain_status
// (0 = active, 1 = suspended) and is enforced directly by every authority point;
// the contact fields are descriptive; MaxUser is enforced at user creation.
type DomainUpdate struct {
	Status    int
	MaxUser   int64
	Title     string
	Address   string
	AdminName string
	Tel       string
}

// GetDomain returns a domain's full record plus its active/inactive/virtual user
// counts, or ok=false when no domain has the id. The counts are derived in the
// same query from each user's address_status nibble and maildir: active = a
// normal mailbox (nibble 0 with a maildir); virtual = a shared mailbox or a row
// with no maildir (lists, contacts); inactive = anything else with a maildir
// (suspended/deleted users).
func (d *SQLDirectory) GetDomain(id int64) (DomainDetail, bool, error) {
	var dd DomainDetail
	err := d.db.QueryRow(`
SELECT d.id, d.domainname, d.org_id, d.domain_status, d.homedir,
       d.max_user, d.title, d.address, d.admin_name, d.tel,
       COALESCE(SUM(CASE WHEN (u.address_status & 15) = 0 AND u.maildir <> '' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN (u.address_status & 15) NOT IN (0, 4) AND u.maildir <> '' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN (u.address_status & 15) = 4 OR u.maildir = '' THEN 1 ELSE 0 END), 0)
  FROM domains d LEFT JOIN users u ON u.domain_id = d.id
 WHERE d.id = ?
 GROUP BY d.id`, id).Scan(
		&dd.ID, &dd.Name, &dd.OrgID, &dd.Status, &dd.Homedir,
		&dd.MaxUser, &dd.Title, &dd.Address, &dd.AdminName, &dd.Tel,
		&dd.ActiveUsers, &dd.InactiveUsers, &dd.VirtualUsers)
	if errors.Is(err, sql.ErrNoRows) {
		return DomainDetail{}, false, nil
	}
	if err != nil {
		return DomainDetail{}, false, err
	}
	return dd, true, nil
}

// UpdateDomain writes a domain's editable fields, reporting whether the domain
// existed. Setting Status to 1 suspends the domain — login (Authenticate),
// delivery (IsLocalDomain), and the address-book queries all read domain_status
// directly, so the change takes effect with no per-user update. Existence is
// checked first (the UPDATE's affected-row count is 0 for an unchanged row, so it
// cannot distinguish a missing domain from an idempotent write).
func (d *SQLDirectory) UpdateDomain(id int64, u DomainUpdate) (bool, error) {
	var exists int64
	err := d.db.QueryRow(`SELECT id FROM domains WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = d.db.Exec(
		`UPDATE domains SET domain_status = ?, max_user = ?, title = ?, address = ?, admin_name = ?, tel = ?
		  WHERE id = ?`,
		u.Status, u.MaxUser, u.Title, u.Address, u.AdminName, u.Tel, id)
	return err == nil, err
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
	var domainID, maxUser int64
	err := d.db.QueryRow(`SELECT id, max_user FROM domains WHERE domainname = ?`, domain).Scan(&domainID, &maxUser)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("directory: domain %q not found", domain)
	}
	if err != nil {
		return 0, err
	}
	// Enforce the domain's mailbox cap (0 = unlimited). The count is every user
	// row in the domain, matching the reference, so distribution lists count too.
	if maxUser > 0 {
		var count int64
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM users WHERE domain_id = ?`, domainID).Scan(&count); err != nil {
			return 0, err
		}
		if count >= maxUser {
			return 0, fmt.Errorf("directory: domain %q has reached its user limit of %d", domain, maxUser)
		}
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

// SetPassword replaces a user's local password hash, reporting whether the user
// existed. For an LDAP-mastered account the stored hash is inert (authentication
// goes to LDAP), but it is updated regardless.
func (d *SQLDirectory) SetPassword(username, password string) (bool, error) {
	hash, err := sqlCryptNewHash(password)
	if err != nil {
		return false, err
	}
	res, err := d.db.Exec(`UPDATE users SET password = ? WHERE username = ?`,
		hash, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UserDetail is a user's full administrative record for the detail/edit view.
// Status is the user-status nibble of address_status (the domain-status bits,
// 0x30, are kept separate); LDAP is true when the account is LDAP-mastered.
type UserDetail struct {
	ID          int64
	Username    string
	DomainID    int64
	Status      int
	Lang        string
	Timezone    string
	DisplayType int
	Homeserver  int
	Maildir     string
	POP3IMAP    bool
	SMTP        bool
	ChgPasswd   bool
	Web         bool
	EAS         bool
	DAV         bool
	LDAP        bool
}

// GetUser returns one user's administrative record, ok=false when no user has
// that username.
func (d *SQLDirectory) GetUser(username string) (UserDetail, bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var u UserDetail
	var addrStatus int
	var priv uint32
	var externid []byte
	err := d.db.QueryRow(
		`SELECT id, username, domain_id, address_status, lang, timezone, privilege_bits, display_type, homeserver, maildir, externid
		   FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.DomainID, &addrStatus, &u.Lang, &u.Timezone, &priv, &u.DisplayType, &u.Homeserver, &u.Maildir, &externid)
	if errors.Is(err, sql.ErrNoRows) {
		return UserDetail{}, false, nil
	}
	if err != nil {
		return UserDetail{}, false, err
	}
	u.Status = addrStatus & 0x0F
	sp := privilegesFromBits(priv)
	u.POP3IMAP, u.SMTP, u.ChgPasswd = sp.POP3IMAP, sp.SMTP, sp.ChgPasswd
	u.Web, u.EAS, u.DAV = sp.Web, sp.EAS, sp.DAV
	u.LDAP = externid != nil
	return u, true, nil
}

// privilegesFromBits decodes the privilege_bits column. POP3/IMAP, SMTP and
// CHGPASSWD are plain bits; WEB/EAS/DAV follow the DETAIL1 opt-out convention, so
// they read as granted unless DETAIL1 is set and the service's own bit is clear.
func privilegesFromBits(priv uint32) ServicePrivileges {
	return ServicePrivileges{
		POP3IMAP:  priv&privIMAPPOP3 != 0,
		SMTP:      priv&privSMTP != 0,
		ChgPasswd: priv&privChgPasswd != 0,
		Web:       priv&(privWeb|privDetail1) != privDetail1,
		EAS:       priv&(privEAS|privDetail1) != privDetail1,
		DAV:       priv&(privDAV|privDetail1) != privDetail1,
	}
}

// Privileges reports a user's permitted login services, derived from
// privilege_bits; ok is false when no user has that username.
func (d *SQLDirectory) Privileges(user string) (ServicePrivileges, bool) {
	u, ok, err := d.GetUser(user)
	if err != nil || !ok {
		return ServicePrivileges{}, false
	}
	return ServicePrivileges{
		POP3IMAP:  u.POP3IMAP,
		SMTP:      u.SMTP,
		ChgPasswd: u.ChgPasswd,
		Web:       u.Web,
		EAS:       u.EAS,
		DAV:       u.DAV,
	}, true
}

// UserUpdate is the editable subset of a user's record. Identity fields
// (username, domain, maildir, externid) are not editable here. Updating replaces
// the whole subset.
type UserUpdate struct {
	Status      int
	Lang        string
	Timezone    string
	DisplayType int
	Homeserver  int
	POP3IMAP    bool
	SMTP        bool
	ChgPasswd   bool
	Web         bool
	EAS         bool
	DAV         bool
}

// UpdateUser writes the editable subset of a user's record, reporting whether the
// user existed. The user-status nibble is replaced while the domain-status bits
// (0x30) are preserved in SQL; privilege bits beyond the two hermEX defines are
// likewise preserved.
func (d *SQLDirectory) UpdateUser(username string, u UserUpdate) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var id int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = d.db.Exec(
		`UPDATE users SET
		   address_status = (address_status & 0x30) | ?,
		   lang = ?, timezone = ?, display_type = ?, homeserver = ?,
		   privilege_bits = (privilege_bits & ?) | ?
		 WHERE username = ?`,
		u.Status&0x0F, u.Lang, u.Timezone, u.DisplayType, u.Homeserver,
		^uint64(managedPrivileges), privilegeBitsFor(u), username)
	return err == nil, err
}

// managedPrivileges is the set of privilege_bits the admin update owns; UpdateUser
// clears exactly these before writing the new value, preserving the reference's
// other bits (Chat/Video/Files/Archive/PubAddr) untouched.
const managedPrivileges = privIMAPPOP3 | privSMTP | privChgPasswd | privDetail1 | privWeb | privEAS | privDAV

// privilegeBitsFor builds the managed privilege_bits for an update: POP3/IMAP,
// SMTP and CHGPASSWD as plain bits, and WEB/EAS/DAV under DETAIL1 (always set so
// the explicit service bits are honored). It is the inverse of privilegesFromBits
// over the managed bits.
func privilegeBitsFor(u UserUpdate) uint64 {
	var priv uint64
	if u.POP3IMAP {
		priv |= privIMAPPOP3
	}
	if u.SMTP {
		priv |= privSMTP
	}
	if u.ChgPasswd {
		priv |= privChgPasswd
	}
	priv |= privDetail1
	if u.Web {
		priv |= privWeb
	}
	if u.EAS {
		priv |= privEAS
	}
	if u.DAV {
		priv |= privDAV
	}
	return priv
}

// DeleteUser removes a user and its dependent rows, reporting whether the user
// existed. altnames and admin_roles cascade via their foreign keys; aliases have
// no FK (mainname is a plain string), so they are deleted explicitly — otherwise
// an orphaned alias would keep its UNIQUE address and block re-creating it. When
// deleteFiles is set the maildir is removed from disk after the row is gone.
func (d *SQLDirectory) DeleteUser(username string, deleteFiles bool) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var maildir string
	err := d.db.QueryRow(`SELECT maildir FROM users WHERE username = ?`, username).Scan(&maildir)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM aliases WHERE mainname = ?`, username); err != nil {
		return false, err
	}
	// forwards.username is a plain string with no FK, so the row is removed explicitly.
	if _, err := tx.Exec(`DELETE FROM forwards WHERE username = ?`, username); err != nil {
		return false, err
	}
	// fetchmail.mailbox is likewise a plain string; remove the entries (the cascade
	// then clears their fetchmail_seen rows).
	if _, err := tx.Exec(`DELETE FROM fetchmail WHERE mailbox = ?`, username); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE username = ?`, username); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if deleteFiles && maildir != "" {
		_ = os.RemoveAll(maildir) // best-effort: the row is gone; an orphaned maildir is harmless
	}
	return true, nil
}

// ListAltnames returns a user's alternative login names, ordered, for the admin
// detail view; an unknown user simply has none.
func (d *SQLDirectory) ListAltnames(username string) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT a.altname FROM altnames a JOIN users u ON a.user_id = u.id
		 WHERE u.username = ? ORDER BY a.altname`,
		strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAltnames replaces a user's alternative login names with the given set
// (lowercased, trimmed, de-duplicated, blanks dropped), reporting whether the
// user existed. The replace runs in one transaction; the altname UNIQUE key
// rejects a name already taken by another account, rolling the change back.
func (d *SQLDirectory) SetAltnames(username string, altnames []string) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var id int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	var clean []string
	for _, a := range altnames {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		clean = append(clean, a)
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM altnames WHERE user_id = ?`, id); err != nil {
		return false, err
	}
	for _, a := range clean {
		if _, err := tx.Exec(`INSERT INTO altnames (user_id, altname) VALUES (?, ?)`, id, a); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// ListAliasesFor returns the e-mail aliases that deliver to a user (the aliases
// whose mainname is the user), ordered, for the admin detail view.
func (d *SQLDirectory) ListAliasesFor(username string) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT aliasname FROM aliases WHERE mainname = ? ORDER BY aliasname`,
		strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAliasesFor replaces the e-mail aliases delivering to a user with the given
// set (lowercased, trimmed, de-duplicated, blanks dropped), reporting whether the
// user existed. The replace runs in one transaction; the aliasname UNIQUE key
// rejects an address already in use, rolling the change back.
func (d *SQLDirectory) SetAliasesFor(username string, aliases []string) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var id int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	var clean []string
	for _, a := range aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		clean = append(clean, a)
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM aliases WHERE mainname = ?`, username); err != nil {
		return false, err
	}
	for _, a := range clean {
		if _, err := tx.Exec(`INSERT INTO aliases (aliasname, mainname) VALUES (?, ?)`, a, username); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// GetForward implements Forwarder: it returns the forward directive of the user the
// address resolves to (its canonical username, an alias, or an altname), or ok=false
// when no forward is set. The lookup is canonical so a forward configured on the
// account applies regardless of which receiving address the mail arrived at — keying
// on the raw alias would let mail to an alias bypass the forward.
func (d *SQLDirectory) GetForward(address string) (ForwardInfo, bool, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	var fi ForwardInfo
	err := d.db.QueryRow(`
SELECT f.forward_type, f.destination FROM forwards f
 WHERE f.username = (
   SELECT u.username FROM users u WHERE u.username = ?
   UNION SELECT u.username FROM users u JOIN altnames a ON a.user_id = u.id WHERE a.altname = ?
   UNION SELECT u.username FROM users u JOIN aliases al ON al.mainname = u.username WHERE al.aliasname = ?
   LIMIT 1)`, addr, addr, addr).Scan(&fi.Type, &fi.Destination)
	if errors.Is(err, sql.ErrNoRows) {
		return ForwardInfo{}, false, nil
	}
	if err != nil {
		return ForwardInfo{}, false, err
	}
	return fi, true, nil
}

// SetForward sets a user's forward directive, keyed by canonical username, reporting
// whether the user existed. An empty destination clears the forward (the "—" / none
// choice). forwardType is ForwardCC or ForwardRedirect; any other value is rejected.
func (d *SQLDirectory) SetForward(username string, forwardType int, destination string) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	destination = strings.ToLower(strings.TrimSpace(destination))
	if destination != "" && forwardType != ForwardCC && forwardType != ForwardRedirect {
		return false, fmt.Errorf("directory: invalid forward type %d", forwardType)
	}
	var id int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if destination == "" {
		_, err := d.db.Exec(`DELETE FROM forwards WHERE username = ?`, username)
		return err == nil, err
	}
	_, err = d.db.Exec(
		`INSERT INTO forwards (username, forward_type, destination) VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE forward_type = VALUES(forward_type), destination = VALUES(destination)`,
		username, forwardType, destination)
	return err == nil, err
}

// GetUserProperties returns a user's string-valued (PtUnicode) MAPI properties,
// keyed by full 32-bit proptag, for the admin contact/detail view. Binary
// properties and multi-valued rows (order_id > 1) are not returned.
func (d *SQLDirectory) GetUserProperties(username string) (map[uint32]string, error) {
	rows, err := d.db.Query(
		`SELECT p.proptag, p.propval_str
		   FROM user_properties p JOIN users u ON p.user_id = u.id
		  WHERE u.username = ? AND p.order_id = 1 AND p.propval_str IS NOT NULL`,
		strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint32]string{}
	for rows.Next() {
		var tag uint32
		var val string
		if err := rows.Scan(&tag, &val); err != nil {
			return nil, err
		}
		out[tag] = val
	}
	return out, rows.Err()
}

// SetUserProperties writes the given string-valued MAPI properties for a user,
// reporting whether the user existed. It touches ONLY the proptags in the map:
// user_properties is a shared EAV table, so a non-empty value upserts that one
// proptag and an empty value deletes it, while every other property of the user
// (creationtime, address-book cloak bits, ...) is left intact. order_id is 1.
func (d *SQLDirectory) SetUserProperties(username string, props map[uint32]string) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var id int64
	err := d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for tag, val := range props {
		val = strings.TrimSpace(val)
		if val == "" {
			if _, err := tx.Exec(
				`DELETE FROM user_properties WHERE user_id = ? AND proptag = ? AND order_id = 1`,
				id, tag); err != nil {
				return false, err
			}
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO user_properties (user_id, proptag, order_id, propval_str, propval_bin) VALUES (?, ?, 1, ?, NULL)
			 ON DUPLICATE KEY UPDATE propval_str = VALUES(propval_str), propval_bin = NULL`,
			id, tag, val); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// CreateAlias maps an alternate address (aliasname) to a canonical user
// (mainname == users.username) in the aliases table.
func (d *SQLDirectory) CreateAlias(aliasname, mainname string) error {
	_, err := d.db.Exec(`INSERT INTO aliases (aliasname, mainname) VALUES (?, ?)`,
		strings.ToLower(strings.TrimSpace(aliasname)), strings.ToLower(strings.TrimSpace(mainname)))
	return err
}

// AliasInfo is an alias address and the primary address it forwards to.
type AliasInfo struct {
	ID    int64
	Alias string
	Main  string
}

// ListAliases returns every alias, ordered by alias address, for the admin API.
func (d *SQLDirectory) ListAliases() ([]AliasInfo, error) {
	rows, err := d.db.Query(`SELECT id, aliasname, mainname FROM aliases ORDER BY aliasname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AliasInfo
	for rows.Next() {
		var ai AliasInfo
		if err := rows.Scan(&ai.ID, &ai.Alias, &ai.Main); err != nil {
			return nil, err
		}
		out = append(out, ai)
	}
	return out, rows.Err()
}

// sqlCryptNewHash produces a sha512-crypt ($6$) hash with a random salt, the
// default credential scheme for the directory.
func sqlCryptNewHash(password string) (string, error) {
	return sha512_crypt.New().Generate([]byte(password), nil)
}

// sqlCryptVerify checks a password against a stored crypt(3) hash, dispatching on
// the hash's scheme prefix so a password set by an external crypt(3) tool
// interoperates: $6$ (sha512-crypt, the directory's own default) and $1$
// (md5-crypt) are both accepted. An empty hash, or one of an unrecognized
// scheme, never matches.
func sqlCryptVerify(password, stored string) bool {
	switch {
	case strings.HasPrefix(stored, "$6$"):
		return sha512_crypt.New().Verify(stored, []byte(password)) == nil
	case strings.HasPrefix(stored, "$1$"):
		return md5_crypt.New().Verify(stored, []byte(password)) == nil
	default:
		return false
	}
}
