package directory

import (
	"database/sql"
	"errors"
)

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
