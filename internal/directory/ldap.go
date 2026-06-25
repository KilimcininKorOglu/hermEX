package directory

import (
	"database/sql"
	"encoding/json"
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
	// SyncFields configures the optional profile downsync: which standard fields are
	// pulled from LDAP and the attribute each reads from (keyed by the field keys in
	// LDAPProfileFields). Empty means only the account's existence and login sync.
	SyncFields map[string]LDAPSyncField
}

// LDAPSyncField is one profile attribute's per-org downsync setting: whether it is
// pulled and the LDAP attribute it reads from (empty = the field's standard
// attribute).
type LDAPSyncField struct {
	Attr    string `json:"attr"`
	Enabled bool   `json:"enabled"`
}

// ldapSyncConfig is the JSON document persisted in ldap_config.sync_config. The
// group-to-list settings are added by the group-sync step; absent keys decode to
// their zero value, so older rows stay valid.
type ldapSyncConfig struct {
	Fields map[string]LDAPSyncField `json:"fields,omitempty"`
}

// ldapProfileField is one syncable AD/LDAP profile attribute and where its value
// lands locally. Proptag 0 marks the binary photo, written to the mailbox store
// (SetUserPhoto), not user_properties.
type ldapProfileField struct {
	Key         string
	DefaultAttr string
	Proptag     uint32
}

// ldapProfileFields is the fixed set of profile attributes a downsync can populate,
// in display order; the photo is last and binary. Proptags are the standard MAPI
// person properties (kept numeric to match the directory's other property code).
var ldapProfileFields = []ldapProfileField{
	{"displayName", "displayName", 0x3001001F},
	{"givenName", "givenName", 0x3A06001F},
	{"surname", "sn", 0x3A11001F},
	{"title", "title", 0x3A17001F},
	{"department", "department", 0x3A18001F},
	{"company", "company", 0x3A16001F},
	{"office", "physicalDeliveryOfficeName", 0x3A19001F},
	{"businessPhone", "telephoneNumber", 0x3A08001F},
	{"mobile", "mobile", 0x3A1C001F},
	{"photo", "thumbnailPhoto", 0},
}

// LDAPPhotoFieldKey is the profile field whose value is a binary portrait written to
// the mailbox store rather than to user_properties.
const LDAPPhotoFieldKey = "photo"

// LDAPProfileFields lists the syncable profile field keys with their standard LDAP
// attribute, in display order, for the admin configuration surface.
func LDAPProfileFields() []struct{ Key, DefaultAttr string } {
	out := make([]struct{ Key, DefaultAttr string }, len(ldapProfileFields))
	for i, f := range ldapProfileFields {
		out[i] = struct{ Key, DefaultAttr string }{f.Key, f.DefaultAttr}
	}
	return out
}

// EnabledProfileSync returns the enabled profile fields as key->attribute, resolving
// each attribute to the org override or the field's standard default. The photo key
// is included; its caller reads it as binary.
func (cfg LDAPConfig) EnabledProfileSync() map[string]string {
	out := make(map[string]string)
	for _, f := range ldapProfileFields {
		s, ok := cfg.SyncFields[f.Key]
		if !ok || !s.Enabled {
			continue
		}
		attr := strings.TrimSpace(s.Attr)
		if attr == "" {
			attr = f.DefaultAttr
		}
		out[f.Key] = attr
	}
	return out
}

// ApplyLDAPProfile writes downsynced profile string values to a user's directory
// properties, mapping each field key to its MAPI proptag. The photo is NOT handled
// here (it lives in the mailbox store). Unknown keys and the photo key are ignored;
// it reports whether the user existed.
func (d *SQLDirectory) ApplyLDAPProfile(username string, values map[string]string) (bool, error) {
	props := make(map[uint32]string)
	for _, f := range ldapProfileFields {
		if f.Proptag == 0 {
			continue // the binary photo, handled by the caller via the store
		}
		if v, ok := values[f.Key]; ok {
			props[f.Proptag] = v
		}
	}
	if len(props) == 0 {
		return true, nil
	}
	return d.SetUserProperties(username, props)
}

// GetLDAPConfig returns an organization's LDAP configuration, reporting ok=false
// when the org has none — in which case its users authenticate against local
// crypt rather than a directory.
func (d *SQLDirectory) GetLDAPConfig(orgID int64) (cfg LDAPConfig, ok bool, err error) {
	var startTLS int
	var syncJSON sql.NullString
	err = d.db.QueryRow(
		`SELECT uri, start_tls, bind_dn, bind_password, base_dn, username_attr, sync_config
		   FROM ldap_config WHERE org_id = ?`, orgID).Scan(
		&cfg.URI, &startTLS, &cfg.BindDN, &cfg.BindPassword, &cfg.BaseDN, &cfg.UsernameAttr, &syncJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return LDAPConfig{}, false, nil
	}
	if err != nil {
		return LDAPConfig{}, false, err
	}
	cfg.StartTLS = startTLS != 0
	if syncJSON.Valid && syncJSON.String != "" {
		var sc ldapSyncConfig
		if err := json.Unmarshal([]byte(syncJSON.String), &sc); err != nil {
			return LDAPConfig{}, false, fmt.Errorf("directory: malformed ldap sync_config: %w", err)
		}
		cfg.SyncFields = sc.Fields
	}
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
	var syncJSON any
	if len(cfg.SyncFields) > 0 {
		b, err := json.Marshal(ldapSyncConfig{Fields: cfg.SyncFields})
		if err != nil {
			return err
		}
		syncJSON = string(b)
	}
	_, err := d.db.Exec(
		`REPLACE INTO ldap_config
			(org_id, uri, start_tls, bind_dn, bind_password, base_dn, username_attr, sync_config)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, cfg.URI, startTLS, cfg.BindDN, cfg.BindPassword, cfg.BaseDN, cfg.UsernameAttr, syncJSON)
	return err
}
