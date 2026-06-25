// Package ldapauth verifies a login against an LDAP/AD directory by
// bind-to-verify: bind as a service account, search for the login's
// distinguished name, then simple-bind as that DN with the supplied password.
// It implements directory.LDAPVerifier, keeping the LDAP client dependency out
// of the directory package.
package ldapauth

import (
	"crypto/tls"
	"fmt"
	"net/url"

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
// describes. An empty password is rejected outright — an empty simple bind is an
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
	c, err := v.dial(cfg.URI)
	if err != nil {
		return nil, err
	}
	if cfg.StartTLS {
		host, _ := hostOf(cfg.URI)
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			c.Close()
			return nil, err
		}
	}
	if cfg.BindDN != "" {
		if err := c.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			c.Close()
			return nil, err
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
// account's externid — objectGUID on Active Directory, entryUUID on OpenLDAP).
type SyncedUser struct {
	Username string
	ExternID []byte
	DN       string            // the entry's distinguished name, for group membership
	Fields   map[string]string // enabled profile string fields, key (not attr) -> value
	Photo    []byte            // the enabled binary portrait, nil when not synced/empty
}

// Sync lists the directory's accounts for downsync into the local directory: it
// searches the base for every entry carrying the login attribute and returns
// each one's login and stable identifier. An entry with no stable identifier is
// skipped — there is nothing to bind its externid to.
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
	res, err := c.Search(ldap.NewSearchRequest(
		cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		fmt.Sprintf("(%s=*)", attr), want, nil))
	if err != nil {
		return nil, err
	}
	out := make([]SyncedUser, 0, len(res.Entries))
	for _, e := range res.Entries {
		login := e.GetAttributeValue(attr)
		if login == "" {
			continue
		}
		id := e.GetRawAttributeValue("objectGUID")
		if len(id) == 0 {
			id = e.GetRawAttributeValue("entryUUID")
		}
		if len(id) == 0 {
			continue
		}
		su := SyncedUser{Username: login, ExternID: id, DN: e.DN}
		for key, a := range profile {
			if key == directory.LDAPPhotoFieldKey {
				if raw := e.GetRawAttributeValue(a); len(raw) > 0 {
					su.Photo = raw
				}
				continue
			}
			if val := e.GetAttributeValue(a); val != "" {
				if su.Fields == nil {
					su.Fields = make(map[string]string)
				}
				su.Fields[key] = val
			}
		}
		out = append(out, su)
	}
	return out, nil
}

// hostOf extracts the host (for the TLS ServerName) from an LDAP URI.
func hostOf(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}
