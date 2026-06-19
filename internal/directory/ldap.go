package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// LDAPVerifier verifies a login against an organization's LDAP directory by
// bind-to-verify (search for the login's DN under the config, then simple-bind as
// it with the supplied password). The LDAP client — which carries the external
// LDAP dependency — implements this; a SQLDirectory with no verifier denies
// LDAP-mastered logins rather than checking them against the local crypt hash.
type LDAPVerifier interface {
	Verify(cfg LDAPConfig, login, password string) (bool, error)
}

// SetLDAPVerifier installs the verifier used for accounts mastered in LDAP (those
// with an externid). Without one, such accounts cannot authenticate.
func (d *SQLDirectory) SetLDAPVerifier(v LDAPVerifier) { d.verifier = v }

// LDAPConfig is one organization's LDAP/AD bind-to-verify configuration: where
// the directory lives, how to reach it, the service account that searches for a
// login's distinguished name, and the attribute a login is matched against. An
// empty URI means the org has no LDAP directory configured.
type LDAPConfig struct {
	URI          string // ldap://host:389 or ldaps://host:636
	StartTLS     bool   // upgrade a plaintext connection with StartTLS
	BindDN       string // service-account DN used for the search phase
	BindPassword string // service-account password
	BaseDN       string // search base for the login lookup
	UsernameAttr string // attribute matched against the login (e.g. "mail")
}

// GetLDAPConfig returns an organization's LDAP configuration, reporting ok=false
// when the org has none — in which case its users authenticate against local
// crypt rather than a directory.
func (d *SQLDirectory) GetLDAPConfig(orgID int64) (cfg LDAPConfig, ok bool, err error) {
	var startTLS int
	err = d.db.QueryRow(
		`SELECT uri, start_tls, bind_dn, bind_password, base_dn, username_attr
		   FROM ldap_config WHERE org_id = ?`, orgID).Scan(
		&cfg.URI, &startTLS, &cfg.BindDN, &cfg.BindPassword, &cfg.BaseDN, &cfg.UsernameAttr)
	if errors.Is(err, sql.ErrNoRows) {
		return LDAPConfig{}, false, nil
	}
	if err != nil {
		return LDAPConfig{}, false, err
	}
	cfg.StartTLS = startTLS != 0
	return cfg, true, nil
}

// UpsertLDAPUser records an account discovered in an LDAP downsync: an existing
// user (matched by username) has its externid set, marking it LDAP-mastered; a
// new user is created with that externid, an empty local password (it
// authenticates only against LDAP), and the given maildir. It reports whether a
// new user was created; the user's domain must already exist.
func (d *SQLDirectory) UpsertLDAPUser(username string, externid []byte, maildir string) (created bool, err error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var id int64
	switch err = d.db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); {
	case err == nil:
		_, err = d.db.Exec(`UPDATE users SET externid = ? WHERE id = ?`, externid, id)
		return false, err
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	at := strings.LastIndexByte(username, '@')
	if at <= 0 {
		return false, errors.New("directory: username must be an email address")
	}
	domain := username[at+1:]
	var domainID int64
	switch err = d.db.QueryRow(`SELECT id FROM domains WHERE domainname = ?`, domain).Scan(&domainID); {
	case errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("directory: domain %q not found", domain)
	case err != nil:
		return false, err
	}
	if _, err = d.db.Exec(
		`INSERT INTO users
		   (username, password, domain_id, homeserver, maildir, lang, timezone, privilege_bits, address_status, display_type, externid)
		 VALUES (?, '', ?, 0, ?, '', '', ?, 0, 0, ?)`,
		username, domainID, maildir, privIMAPPOP3|privSMTP, externid); err != nil {
		return false, err
	}
	if maildir != "" {
		if err = os.MkdirAll(maildir, 0o700); err != nil {
			return false, err
		}
	}
	return true, nil
}

// SetLDAPConfig stores (replacing any existing) an organization's LDAP
// configuration.
func (d *SQLDirectory) SetLDAPConfig(orgID int64, cfg LDAPConfig) error {
	startTLS := 0
	if cfg.StartTLS {
		startTLS = 1
	}
	_, err := d.db.Exec(
		`REPLACE INTO ldap_config
			(org_id, uri, start_tls, bind_dn, bind_password, base_dn, username_attr)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		orgID, cfg.URI, startTLS, cfg.BindDN, cfg.BindPassword, cfg.BaseDN, cfg.UsernameAttr)
	return err
}
