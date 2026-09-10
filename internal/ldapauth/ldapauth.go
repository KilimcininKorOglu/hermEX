// Package ldapauth verifies a login against an LDAP/AD directory by
// bind-to-verify: bind as a service account, search for the login's
// distinguished name, then simple-bind as that DN with the supplied password.
// It implements directory.LDAPVerifier, keeping the LDAP client dependency out
// of the directory package.
package ldapauth

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"hermex/internal/directory"
)

// conn is the subset of *ldap.Conn the verifier uses, abstracted so the
// bind/search orchestration is testable without a live directory.
type conn interface {
	StartTLS(*tls.Config) error
	Bind(username, password string) error
	Search(*ldap.SearchRequest) (*ldap.SearchResult, error)
	Close() error
}

// Verifier authenticates logins against an LDAP directory. Build one with New.
type Verifier struct {
	dial func(uri string) (conn, error)
}

// New returns a Verifier that dials real LDAP servers.
func New() *Verifier {
	return &Verifier{dial: func(uri string) (conn, error) { return ldap.DialURL(uri) }}
}

// Verify reports whether login/password authenticate against the directory cfg
// describes. An empty password is rejected outright, an empty simple bind is an
// unauthenticated (anonymous) bind a server would accept, which must never pass
// for a password check. A directory or system error is returned as such; a
// failed user bind or an absent/ambiguous account is a clean false.
func (v *Verifier) Verify(cfg directory.LDAPConfig, login, password string) (bool, error) {
	if password == "" || cfg.URI == "" {
		return false, nil
	}
	c, err := v.connect(cfg)
	if err != nil {
		return false, err
	}
	defer c.Close()

	// Resolve the login to exactly one distinguished name.
	res, err := c.Search(ldap.NewSearchRequest(
		cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 10, false,
		fmt.Sprintf("(%s=%s)", loginAttr(cfg), ldap.EscapeFilter(login)),
		[]string{"dn"}, nil))
	if err != nil {
		return false, err
	}
	if len(res.Entries) != 1 {
		return false, nil
	}
	// Bind as the user: a successful bind means the password is correct.
	if err := c.Bind(res.Entries[0].DN, password); err != nil {
		return false, nil
	}
	return true, nil
}

// connect dials the directory, optionally upgrades with StartTLS, and binds the
// search service account (an anonymous bind when none is configured). The caller
// closes the returned connection.
func (v *Verifier) connect(cfg directory.LDAPConfig) (conn, error) {
	// Refuse a plaintext session outright. Verifying a password means binding as
	// the user, so a plain bind hands every LDAP-mastered account's real mailbox
	// password to anyone on the path. A stored configuration predating the save
	// check would otherwise keep doing exactly that, silently.
	if !cfg.EncryptedTransport() {
		return nil, directory.ErrInsecureLDAP
	}
	c, err := v.dial(cfg.URI)
	if err != nil {
		return nil, err
	}
	if cfg.StartTLS {
		host, _ := hostOf(cfg.URI)
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return nil, errors.Join(err, c.Close())
		}
	}
	if cfg.BindDN != "" {
		if err := c.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			return nil, errors.Join(err, c.Close())
		}
	}
	return c, nil
}

// loginAttr is the directory attribute a login is matched against (the login's
// e-mail address by default).
func loginAttr(cfg directory.LDAPConfig) string {
	if cfg.UsernameAttr != "" {
		return cfg.UsernameAttr
	}
	return "mail"
}

// SyncedUser is one account discovered in the directory: its login (the value of
// the configured login attribute) and the directory's stable identifier (the
// account's externid, objectGUID on Active Directory, entryUUID on OpenLDAP).
type SyncedUser struct {
	Username string
	ExternID []byte
	DN       string            // the entry's distinguished name, for group membership
	Fields   map[string]string // enabled profile string fields, key (not attr) -> value
	Photo    []byte            // the enabled binary portrait, nil when not synced/empty
	Aliases  []string          // the account's additional addresses, empty when not synced
}

// Sync lists the directory's accounts for downsync into the local directory: it
// searches the base for every entry carrying the login attribute and returns
// each one's login and stable identifier. An entry with no stable identifier is
// skipped, there is nothing to bind its externid to.
func (v *Verifier) Sync(cfg directory.LDAPConfig) ([]SyncedUser, error) {
	c, err := v.connect(cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	attr := loginAttr(cfg)
	profile := cfg.EnabledProfileSync() // field key -> LDAP attribute
	want := []string{attr, "objectGUID", "entryUUID"}
	for _, a := range profile {
		want = append(want, a)
	}
	if cfg.AliasAttr != "" {
		want = append(want, cfg.AliasAttr)
	}
	res, err := c.Search(ldap.NewSearchRequest(
		cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		fmt.Sprintf("(%s=*)", attr), want, nil))
	if err != nil {
		return nil, err
	}
	out := make([]SyncedUser, 0, len(res.Entries))
	for _, e := range res.Entries {
		su, ok := syncedUser(e, attr, profile)
		if !ok {
			continue
		}
		su.Aliases = aliasAddresses(e.GetAttributeValues(cfg.AliasAttr), su.Username)
		out = append(out, su)
	}
	return out, nil
}

// syncedUser reads one directory entry. An entry with no login, or with no stable
// identifier to bind its externid to, is skipped.
func syncedUser(e *ldap.Entry, attr string, profile map[string]string) (SyncedUser, bool) {
	login := e.GetAttributeValue(attr)
	if login == "" {
		return SyncedUser{}, false
	}
	id := e.GetRawAttributeValue("objectGUID")
	if len(id) == 0 {
		id = e.GetRawAttributeValue("entryUUID")
	}
	if len(id) == 0 {
		return SyncedUser{}, false
	}
	su := SyncedUser{Username: login, ExternID: id, DN: e.DN}
	readProfile(&su, e, profile)
	return su, true
}

// aliasAddresses turns raw alias attribute values into deliverable addresses. Active
// Directory publishes proxyAddresses as scheme-prefixed values ("SMTP:" for the primary
// address, "smtp:" for the others, plus non-mail schemes such as "x500:" and "sip:"), so a
// value carrying a scheme is kept only when that scheme is smtp, and the address itself is
// lower-cased. The account's own login is dropped: it is the address, not an alias of it.
func aliasAddresses(values []string, login string) []string {
	login = strings.ToLower(strings.TrimSpace(login))
	out := make([]string, 0, len(values))
	for _, v := range values {
		addr, ok := aliasAddress(v)
		if !ok || addr == login {
			continue
		}
		out = append(out, addr)
	}
	return out
}

// aliasAddress strips an smtp: scheme from one alias value and lower-cases the address,
// reporting false for a value carrying any other scheme.
func aliasAddress(value string) (string, bool) {
	value = strings.TrimSpace(value)
	scheme, rest, found := strings.Cut(value, ":")
	if !found {
		return strings.ToLower(value), value != ""
	}
	if !strings.EqualFold(scheme, "smtp") {
		return "", false
	}
	addr := strings.ToLower(rest)
	return addr, addr != ""
}

// readProfile fills the entry's enabled profile fields, routing the portrait to its
// own field rather than the string map.
func readProfile(su *SyncedUser, e *ldap.Entry, profile map[string]string) {
	for key, a := range profile {
		if key == directory.LDAPPhotoFieldKey {
			if raw := e.GetRawAttributeValue(a); len(raw) > 0 {
				su.Photo = raw
			}
			continue
		}
		val := e.GetAttributeValue(a)
		if val == "" {
			continue
		}
		if su.Fields == nil {
			su.Fields = make(map[string]string)
		}
		su.Fields[key] = val
	}
}

// SyncedGroup is one mail-bearing group discovered in the directory: its address,
// the owner's DN (managedBy), and its members' DNs. The owner and member DNs are
// resolved to local addresses by the caller.
type SyncedGroup struct {
	Mail      string
	OwnerDN   string
	MemberDNs []string
}

// SyncGroups lists the directory's groups for downsync into local distribution
// lists: it searches under the group base (or the login base) with the configured
// filter (default: mail-bearing AD groups) and returns each group's address, owner
// DN, and member DNs. The owner and member DNs are resolved by the caller. Returns
// nil when group sync is disabled.
func (v *Verifier) SyncGroups(cfg directory.LDAPConfig) ([]SyncedGroup, error) {
	if !cfg.SyncGroups {
		return nil, nil
	}
	c, err := v.connect(cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	base := cfg.GroupBaseDN
	if base == "" {
		base = cfg.BaseDN
	}
	filter := strings.TrimSpace(cfg.GroupFilter)
	if filter == "" {
		filter = "(&(objectClass=group)(mail=*))"
	}
	res, err := c.Search(ldap.NewSearchRequest(
		base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		filter, []string{"mail", "managedBy", "member"}, nil))
	if err != nil {
		return nil, err
	}
	out := make([]SyncedGroup, 0, len(res.Entries))
	for _, e := range res.Entries {
		mail := e.GetAttributeValue("mail")
		if mail == "" {
			continue // a group with no address cannot become a distribution list
		}
		out = append(out, SyncedGroup{
			Mail:      mail,
			OwnerDN:   e.GetAttributeValue("managedBy"),
			MemberDNs: e.GetAttributeValues("member"),
		})
	}
	return out, nil
}

// SyncedContact is one mail-bearing contact discovered in the directory: its address,
// its display name, and the directory's stable identifier for it. A contact is a GAL
// entry for an external address; it owns no mailbox and never logs in.
type SyncedContact struct {
	Mail        string
	DisplayName string
	ExternID    []byte
}

// SyncContacts lists the directory's mail contacts for downsync into local org mail
// contacts: it searches under the contact base (or the login base) with the configured
// filter (default: mail-bearing contact objects) and returns each contact's address,
// display name and stable identifier. An entry with no address, or with no stable
// identifier to bind its externid to, is skipped. Returns nil when contact sync is
// disabled.
func (v *Verifier) SyncContacts(cfg directory.LDAPConfig) ([]SyncedContact, error) {
	if !cfg.SyncContacts {
		return nil, nil
	}
	c, err := v.connect(cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	base := cfg.ContactBaseDN
	if base == "" {
		base = cfg.BaseDN
	}
	filter := strings.TrimSpace(cfg.ContactFilter)
	if filter == "" {
		filter = "(&(objectClass=contact)(mail=*))"
	}
	res, err := c.Search(ldap.NewSearchRequest(
		base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		filter, []string{"mail", "displayName", "cn", "objectGUID", "entryUUID"}, nil))
	if err != nil {
		return nil, err
	}
	out := make([]SyncedContact, 0, len(res.Entries))
	for _, e := range res.Entries {
		sc, ok := syncedContact(e)
		if !ok {
			continue
		}
		out = append(out, sc)
	}
	return out, nil
}

// syncedContact reads one contact entry, taking the display name from displayName and
// falling back to cn. It reports false for an entry the downsync cannot file.
func syncedContact(e *ldap.Entry) (SyncedContact, bool) {
	mail := e.GetAttributeValue("mail")
	if mail == "" {
		return SyncedContact{}, false // no address, nothing to put in the GAL
	}
	id := e.GetRawAttributeValue("objectGUID")
	if len(id) == 0 {
		id = e.GetRawAttributeValue("entryUUID")
	}
	if len(id) == 0 {
		return SyncedContact{}, false
	}
	name := e.GetAttributeValue("displayName")
	if name == "" {
		name = e.GetAttributeValue("cn")
	}
	return SyncedContact{Mail: mail, DisplayName: name, ExternID: id}, true
}

// hostOf extracts the host (for the TLS ServerName) from an LDAP URI.
func hostOf(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}
