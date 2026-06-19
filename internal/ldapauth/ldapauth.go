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
	c, err := v.dial(cfg.URI)
	if err != nil {
		return false, err
	}
	defer c.Close()

	if cfg.StartTLS {
		host, _ := hostOf(cfg.URI)
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return false, err
		}
	}
	// Bind as the search service account (anonymous when none is configured).
	if cfg.BindDN != "" {
		if err := c.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			return false, err
		}
	}
	// Resolve the login to exactly one distinguished name.
	attr := cfg.UsernameAttr
	if attr == "" {
		attr = "mail"
	}
	res, err := c.Search(ldap.NewSearchRequest(
		cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 10, false,
		fmt.Sprintf("(%s=%s)", attr, ldap.EscapeFilter(login)),
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

// hostOf extracts the host (for the TLS ServerName) from an LDAP URI.
func hostOf(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}
