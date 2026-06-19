package ldapauth

import (
	"crypto/tls"
	"errors"
	"testing"

	"github.com/go-ldap/ldap/v3"

	"hermex/internal/directory"
)

// fakeConn is a scripted LDAP connection: it records the DN of every Bind and
// returns canned search results and per-DN bind verdicts, so the verifier's
// bind/search orchestration is exercised without a live directory.
type fakeConn struct {
	startTLSErr error
	binds       []string         // DN of each Bind call, in order
	bindErr     map[string]error // DN -> error returned by Bind (absent = success)
	searchRes   *ldap.SearchResult
	searchErr   error
	closed      bool
}

func (f *fakeConn) StartTLS(*tls.Config) error { return f.startTLSErr }
func (f *fakeConn) Bind(dn, _ string) error    { f.binds = append(f.binds, dn); return f.bindErr[dn] }
func (f *fakeConn) Search(*ldap.SearchRequest) (*ldap.SearchResult, error) {
	return f.searchRes, f.searchErr
}
func (f *fakeConn) Close() error { f.closed = true; return nil }

func verifierWith(fc *fakeConn) *Verifier {
	return &Verifier{dial: func(string) (conn, error) { return fc, nil }}
}

func oneEntry(dn string) *ldap.SearchResult {
	return &ldap.SearchResult{Entries: []*ldap.Entry{{DN: dn}}}
}

var cfg = directory.LDAPConfig{
	URI:          "ldap://ad.hermex.test:389",
	BindDN:       "cn=svc,dc=hermex,dc=test",
	BindPassword: "svc",
	BaseDN:       "dc=hermex,dc=test",
	UsernameAttr: "mail",
}

// TestVerifyEmptyPasswordRejected proves an empty password is refused before any
// directory contact — an empty simple bind is an anonymous bind a server accepts.
func TestVerifyEmptyPasswordRejected(t *testing.T) {
	fc := &fakeConn{}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "")
	if ok || err != nil {
		t.Errorf("empty password = (%v, %v), want (false, nil)", ok, err)
	}
	if len(fc.binds) != 0 {
		t.Error("empty password must not reach the directory")
	}
}

// TestVerifySuccess proves a resolvable login whose user bind succeeds
// authenticates, binding the service account first then the user.
func TestVerifySuccess(t *testing.T) {
	fc := &fakeConn{searchRes: oneEntry("uid=alice,dc=hermex,dc=test")}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "correct")
	if !ok || err != nil {
		t.Fatalf("Verify = (%v, %v), want (true, nil)", ok, err)
	}
	if len(fc.binds) != 2 || fc.binds[0] != "cn=svc,dc=hermex,dc=test" || fc.binds[1] != "uid=alice,dc=hermex,dc=test" {
		t.Errorf("bind order = %v, want [service, user]", fc.binds)
	}
	if !fc.closed {
		t.Error("the connection was not closed")
	}
}

// TestVerifyWrongPassword proves a failed user bind is a clean false, not an error.
func TestVerifyWrongPassword(t *testing.T) {
	fc := &fakeConn{
		searchRes: oneEntry("uid=alice,dc=hermex,dc=test"),
		bindErr:   map[string]error{"uid=alice,dc=hermex,dc=test": errors.New("invalid credentials")},
	}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "wrong")
	if ok || err != nil {
		t.Errorf("wrong password = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestVerifyNotFound proves a login with no directory entry does not authenticate.
func TestVerifyNotFound(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{}}
	if ok, _ := verifierWith(fc).Verify(cfg, "ghost@hermex.test", "x"); ok {
		t.Error("a login with no entry must not authenticate")
	}
}

// TestVerifyAmbiguous proves a login matching more than one entry does not
// authenticate (it is not safe to pick one).
func TestVerifyAmbiguous(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{{DN: "a"}, {DN: "b"}}}}
	if ok, _ := verifierWith(fc).Verify(cfg, "dup@hermex.test", "x"); ok {
		t.Error("an ambiguous login must not authenticate")
	}
}

// TestVerifyServiceBindError proves a failed service bind is a returned error (a
// configuration/availability fault), distinct from a clean credential rejection.
func TestVerifyServiceBindError(t *testing.T) {
	fc := &fakeConn{bindErr: map[string]error{"cn=svc,dc=hermex,dc=test": errors.New("service bind failed")}}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "x")
	if ok || err == nil {
		t.Errorf("service bind failure = (%v, %v), want (false, error)", ok, err)
	}
}
