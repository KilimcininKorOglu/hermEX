package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// wrapLDAPBind is the wrapping context of a connection's service-account password.
const wrapLDAPBind = "ldap-bind-password"

// mysqlDuplicateKey is the MariaDB error number of a UNIQUE key violation.
const mysqlDuplicateKey = 1062

// ErrLDAPDomainBound refuses a second binding for a domain: a domain syncs from and
// authenticates against exactly one connection.
var ErrLDAPDomainBound = errors.New("directory: the domain is already bound to an LDAP connection")

// ErrLDAPConnectionName refuses a connection with an empty or already used name.
var ErrLDAPConnectionName = errors.New("directory: an LDAP connection needs a unique name")

// ErrLDAPPreset refuses a binding naming a preset this build does not know.
var ErrLDAPPreset = errors.New("directory: unknown LDAP mapping preset")

// LDAPConnection is one reachable LDAP/AD directory: its address, transport, the
// service account that searches it, the default search base and the attribute a
// login is matched against. Several domains can bind to one connection.
type LDAPConnection struct {
	ID           int64
	Name         string
	URI          string // ldap://host:389 or ldaps://host:636
	StartTLS     bool   // upgrade a plaintext connection with StartTLS
	BindDN       string // service-account DN used for the search phase
	BindPassword string // service-account password (plaintext in memory, wrapped at rest)
	BaseDN       string // default search base; a binding may override it
	UsernameAttr string // attribute matched against the login (e.g. "mail")
}

// LDAPBinding attaches one local domain to one connection with that domain's
// downsync mapping. Preset names the mapping template it started from ("" = none).
type LDAPBinding struct {
	ID           int64
	ConnectionID int64
	DomainID     int64
	Domain       string // the bound domain's name, filled on read
	Preset       string
	Mapping      LDAPMapping
}

// transport returns the connection's reachability fields as an LDAPConfig, so the
// transport check is the one EncryptedTransport rule.
func (c LDAPConnection) transport() LDAPConfig {
	return LDAPConfig{URI: c.URI, StartTLS: c.StartTLS}
}

// validate refuses a connection that has no name, no address, or would bind in the
// clear.
func (c LDAPConnection) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return ErrLDAPConnectionName
	}
	if strings.TrimSpace(c.URI) == "" {
		return errors.New("directory: an LDAP connection needs a URI")
	}
	if !c.transport().EncryptedTransport() {
		return ErrInsecureLDAP
	}
	return nil
}

// isDuplicateKey reports whether err is a UNIQUE key violation.
func isDuplicateKey(err error) bool {
	var me *mysqldriver.MySQLError
	return errors.As(err, &me) && me.Number == mysqlDuplicateKey
}

// CreateLDAPConnection stores a new connection and returns its id. The bind
// password is wrapped at rest; an insecure transport or a duplicate name is refused.
func (d *SQLDirectory) CreateLDAPConnection(c LDAPConnection) (int64, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	stored, err := d.wrapKey(wrapLDAPBind, c.BindPassword)
	if err != nil {
		return 0, err
	}
	res, err := d.db.Exec(
		`INSERT INTO ldap_connections (name, uri, start_tls, bind_dn, bind_password, base_dn, username_attr)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(c.Name), c.URI, c.StartTLS, c.BindDN, stored, c.BaseDN, c.UsernameAttr)
	if isDuplicateKey(err) {
		return 0, ErrLDAPConnectionName
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateLDAPConnection replaces every field of an existing connection, the bind
// password included, reporting found=false for an unknown id.
func (d *SQLDirectory) UpdateLDAPConnection(c LDAPConnection) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	stored, err := d.wrapKey(wrapLDAPBind, c.BindPassword)
	if err != nil {
		return false, err
	}
	res, err := d.db.Exec(
		`UPDATE ldap_connections SET name = ?, uri = ?, start_tls = ?, bind_dn = ?, bind_password = ?,
		        base_dn = ?, username_attr = ?
		  WHERE id = ?`,
		strings.TrimSpace(c.Name), c.URI, c.StartTLS, c.BindDN, stored, c.BaseDN, c.UsernameAttr, c.ID)
	if isDuplicateKey(err) {
		return false, ErrLDAPConnectionName
	}
	if err != nil {
		return false, err
	}
	return d.connectionExists(res, c.ID)
}

// connectionExists turns an UPDATE result into found: MariaDB reports zero affected
// rows for an update that changed nothing, so a zero is confirmed against the table.
func (d *SQLDirectory) connectionExists(res sql.Result, id int64) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return n > 0, err
	}
	var one int
	err = d.db.QueryRow(`SELECT 1 FROM ldap_connections WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// GetLDAPConnection returns one connection with its bind password unwrapped,
// found=false for an unknown id. A plaintext password read while a key secret is
// installed is written back wrapped.
func (d *SQLDirectory) GetLDAPConnection(id int64) (LDAPConnection, bool, error) {
	var c LDAPConnection
	var stored string
	err := d.db.QueryRow(
		`SELECT id, name, uri, start_tls, bind_dn, bind_password, base_dn, username_attr
		   FROM ldap_connections WHERE id = ?`, id).Scan(
		&c.ID, &c.Name, &c.URI, &c.StartTLS, &c.BindDN, &stored, &c.BaseDN, &c.UsernameAttr)
	if errors.Is(err, sql.ErrNoRows) {
		return LDAPConnection{}, false, nil
	}
	if err != nil {
		return LDAPConnection{}, false, err
	}
	password, err := d.openLDAPBindPassword(c.ID, stored)
	if err != nil {
		return LDAPConnection{}, false, err
	}
	c.BindPassword = password
	return c, true, nil
}

// openLDAPBindPassword unwraps a stored bind password and, when the row is still
// plaintext and a secret is installed, rewrites it wrapped. The rewrite is
// best-effort: the password was read, so a failed write must not fail the login or
// sync that needs it; the failure is logged and the next read retries.
func (d *SQLDirectory) openLDAPBindPassword(id int64, stored string) (string, error) {
	password, wrapped, err := d.unwrapKey(wrapLDAPBind, stored)
	if err != nil {
		return "", err
	}
	if d.rewrapNeeded(wrapped) {
		if err := d.rewrapLDAPBindPassword(id, stored, password); err != nil {
			log.Printf("directory: could not wrap the bind password of LDAP connection %d: %v", id, err)
		}
	}
	return password, nil
}

// rewrapLDAPBindPassword writes a plaintext bind password back wrapped, only while
// the row still holds the plaintext it was read with (compare-and-set), so a
// concurrent password change is never overwritten.
func (d *SQLDirectory) rewrapLDAPBindPassword(id int64, stored, password string) error {
	sealed, err := d.wrapKey(wrapLDAPBind, password)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`UPDATE ldap_connections SET bind_password = ? WHERE id = ? AND bind_password = ?`,
		sealed, id, stored)
	return err
}

// wrapLDAPBindPasswords wraps every plaintext bind password once a key secret is
// installed, so rows copied by the migration do not wait for their first read. It
// runs from EnsureSchema; a failure is logged rather than returned, because the
// read path wraps the same rows lazily and a daemon must not refuse to start over it.
func (d *SQLDirectory) wrapLDAPBindPasswords() {
	if _, ok := d.wrapping(); !ok {
		return
	}
	plain, err := d.plaintextLDAPBindPasswords()
	if err != nil {
		log.Printf("directory: could not list LDAP bind passwords to wrap: %v", err)
		return
	}
	for id, stored := range plain {
		if err := d.rewrapLDAPBindPassword(id, stored, stored); err != nil {
			log.Printf("directory: could not wrap the bind password of LDAP connection %d: %v", id, err)
		}
	}
}

// plaintextLDAPBindPasswords lists the connections whose bind password is stored
// without the wrap prefix, keyed by connection id.
func (d *SQLDirectory) plaintextLDAPBindPasswords() (map[int64]string, error) {
	rows, err := d.db.Query(
		`SELECT id, bind_password FROM ldap_connections WHERE bind_password <> '' AND bind_password NOT LIKE ?`,
		wrapPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]string)
	for rows.Next() {
		var id int64
		var stored string
		if err := rows.Scan(&id, &stored); err != nil {
			return nil, err
		}
		out[id] = stored
	}
	return out, rows.Err()
}

// ListLDAPConnections returns every connection ordered by name. BindPassword is
// left empty: a listing never needs the secret, and leaving it out keeps a list
// readable even when one row cannot be unwrapped.
func (d *SQLDirectory) ListLDAPConnections() ([]LDAPConnection, error) {
	rows, err := d.db.Query(
		`SELECT id, name, uri, start_tls, bind_dn, base_dn, username_attr FROM ldap_connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LDAPConnection
	for rows.Next() {
		var c LDAPConnection
		if err := rows.Scan(&c.ID, &c.Name, &c.URI, &c.StartTLS, &c.BindDN, &c.BaseDN, &c.UsernameAttr); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteLDAPConnection removes a connection and, by foreign key, every binding to
// it, reporting whether a row went.
func (d *SQLDirectory) DeleteLDAPConnection(id int64) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM ldap_connections WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CreateLDAPBinding binds a domain to a connection and returns the binding id. A
// domain that is already bound is refused with ErrLDAPDomainBound, and an unknown
// preset with ErrLDAPPreset.
func (d *SQLDirectory) CreateLDAPBinding(b LDAPBinding) (int64, error) {
	syncJSON, err := bindingSyncJSON(b)
	if err != nil {
		return 0, err
	}
	res, err := d.db.Exec(
		`INSERT INTO ldap_bindings (connection_id, domain_id, preset, sync_config) VALUES (?, ?, ?, ?)`,
		b.ConnectionID, b.DomainID, b.Preset, syncJSON)
	if isDuplicateKey(err) {
		return 0, ErrLDAPDomainBound
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateLDAPBinding replaces a binding's connection, preset and mapping (the domain
// stays), reporting found=false for an unknown id.
func (d *SQLDirectory) UpdateLDAPBinding(b LDAPBinding) (bool, error) {
	syncJSON, err := bindingSyncJSON(b)
	if err != nil {
		return false, err
	}
	res, err := d.db.Exec(
		`UPDATE ldap_bindings SET connection_id = ?, preset = ?, sync_config = ? WHERE id = ?`,
		b.ConnectionID, b.Preset, syncJSON, b.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return n > 0, err
	}
	_, found, err := d.GetLDAPBinding(b.ID)
	return found, err
}

// bindingSyncJSON validates a binding's preset and encodes its mapping.
func bindingSyncJSON(b LDAPBinding) (any, error) {
	if b.Preset != "" {
		if _, ok := LDAPPreset(b.Preset); !ok {
			return nil, ErrLDAPPreset
		}
	}
	return marshalMapping(b.Mapping)
}

// bindingByID, bindingByDomain and bindingsOfConnection read bindings with their
// domain name, in the column order scanBinding expects. Each is spelled out in
// full so no query text is assembled at run time.
const (
	bindingByID = `SELECT b.id, b.connection_id, b.domain_id, d.domainname, b.preset, b.sync_config
  FROM ldap_bindings b JOIN domains d ON d.id = b.domain_id WHERE b.id = ?`
	bindingByDomain = `SELECT b.id, b.connection_id, b.domain_id, d.domainname, b.preset, b.sync_config
  FROM ldap_bindings b JOIN domains d ON d.id = b.domain_id WHERE b.domain_id = ?`
	bindingsOfConnection = `SELECT b.id, b.connection_id, b.domain_id, d.domainname, b.preset, b.sync_config
  FROM ldap_bindings b JOIN domains d ON d.id = b.domain_id
  WHERE (? = 0 OR b.connection_id = ?) ORDER BY d.domainname`
)

// scanBinding reads one binding row.
func scanBinding(sc interface{ Scan(...any) error }) (LDAPBinding, error) {
	var b LDAPBinding
	var syncJSON sql.NullString
	if err := sc.Scan(&b.ID, &b.ConnectionID, &b.DomainID, &b.Domain, &b.Preset, &syncJSON); err != nil {
		return LDAPBinding{}, err
	}
	if syncJSON.Valid && syncJSON.String != "" {
		if err := json.Unmarshal([]byte(syncJSON.String), &b.Mapping); err != nil {
			return LDAPBinding{}, fmt.Errorf("directory: malformed sync_config of LDAP binding %d: %w", b.ID, err)
		}
	}
	return b, nil
}

// getBinding reads the one binding a single-row query finds, found=false when none.
func (d *SQLDirectory) getBinding(query string, arg int64) (LDAPBinding, bool, error) {
	b, err := scanBinding(d.db.QueryRow(query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return LDAPBinding{}, false, nil
	}
	if err != nil {
		return LDAPBinding{}, false, err
	}
	return b, true, nil
}

// GetLDAPBinding returns one binding, found=false for an unknown id.
func (d *SQLDirectory) GetLDAPBinding(id int64) (LDAPBinding, bool, error) {
	return d.getBinding(bindingByID, id)
}

// LDAPBindingForDomain returns the binding of a domain, found=false when the domain
// is not bound.
func (d *SQLDirectory) LDAPBindingForDomain(domainID int64) (LDAPBinding, bool, error) {
	return d.getBinding(bindingByDomain, domainID)
}

// ListLDAPBindings returns the bindings of one connection, or of every connection
// when connectionID is 0, ordered by domain name.
func (d *SQLDirectory) ListLDAPBindings(connectionID int64) ([]LDAPBinding, error) {
	rows, err := d.db.Query(bindingsOfConnection, connectionID, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LDAPBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteLDAPBinding unbinds a domain, reporting whether a row went. The domain's
// LDAP-mastered users keep their externid and cannot log in until it is bound again.
func (d *SQLDirectory) DeleteLDAPBinding(id int64) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM ldap_bindings WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// LDAPConfigForDomain resolves the effective configuration a domain's logins and
// downsync use: its binding's connection with the binding's mapping. ok=false when
// the domain is not bound.
func (d *SQLDirectory) LDAPConfigForDomain(domainID int64) (LDAPConfig, bool, error) {
	b, found, err := d.LDAPBindingForDomain(domainID)
	if err != nil || !found {
		return LDAPConfig{}, false, err
	}
	return d.configForBinding(b)
}

// LDAPConfigForBinding resolves the effective configuration of one binding,
// ok=false for an unknown id.
func (d *SQLDirectory) LDAPConfigForBinding(bindingID int64) (LDAPConfig, bool, error) {
	b, found, err := d.GetLDAPBinding(bindingID)
	if err != nil || !found {
		return LDAPConfig{}, false, err
	}
	return d.configForBinding(b)
}

// configForBinding loads a binding's connection and merges the two.
func (d *SQLDirectory) configForBinding(b LDAPBinding) (LDAPConfig, bool, error) {
	c, found, err := d.GetLDAPConnection(b.ConnectionID)
	if err != nil {
		return LDAPConfig{}, false, err
	}
	if !found {
		// The foreign key cascades a deleted connection's bindings, so a binding
		// without one is an inconsistency worth reporting rather than a plain miss.
		return LDAPConfig{}, false, fmt.Errorf("directory: LDAP binding %d names missing connection %d", b.ID, b.ConnectionID)
	}
	return effectiveConfig(c, b), true, nil
}

// effectiveConfig merges a connection and one of its bindings into the
// configuration the verifier and the downsync consume: the connection supplies the
// transport, service account and login attribute, the binding the mapping, the
// domain and, when set, the search base.
func effectiveConfig(c LDAPConnection, b LDAPBinding) LDAPConfig {
	m := b.Mapping
	cfg := LDAPConfig{
		URI: c.URI, StartTLS: c.StartTLS, BindDN: c.BindDN, BindPassword: c.BindPassword,
		BaseDN: c.BaseDN, UsernameAttr: c.UsernameAttr,
		SyncFields: m.Fields, AliasAttr: m.AliasAttr,
		SyncGroups: m.SyncGroups, GroupBaseDN: m.GroupBaseDN, GroupFilter: m.GroupFilter,
		SyncContacts: m.SyncContacts, ContactBaseDN: m.ContactBaseDN, ContactFilter: m.ContactFilter,
		DomainID: b.DomainID, Domain: b.Domain,
	}
	if strings.TrimSpace(m.BaseDN) != "" {
		cfg.BaseDN = m.BaseDN
	}
	return cfg
}
