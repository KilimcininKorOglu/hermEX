package ldapauth

import (
	"bytes"
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
	dialed      bool
	startTLSErr error
	tlsConfigs  []*tls.Config    // the configuration of each StartTLS call
	binds       []string         // DN of each Bind call, in order
	bindErr     map[string]error // DN -> error returned by Bind (absent = success)
	searchRes   *ldap.SearchResult
	searchErr   error
	filters     []string // the filter of each Search call, in order
	closed      bool
	closeErr    error
}

func (f *fakeConn) StartTLS(c *tls.Config) error {
	f.tlsConfigs = append(f.tlsConfigs, c)
	return f.startTLSErr
}
func (f *fakeConn) Bind(dn, _ string) error { f.binds = append(f.binds, dn); return f.bindErr[dn] }
func (f *fakeConn) Search(r *ldap.SearchRequest) (*ldap.SearchResult, error) {
	f.filters = append(f.filters, r.Filter)
	return f.searchRes, f.searchErr
}
func (f *fakeConn) Close() error { f.closed = true; return f.closeErr }

// TestVerifyEscapesTheLoginInTheFilter logs in with a name made of the filter
// metacharacters. RFC 4515 section 3 writes '*', '(', ')', '\' and NUL in an
// assertion value as a backslash and two hex digits; unescaped, "*" alone would
// match every entry carrying the attribute, so a login name could pick an account.
func TestVerifyEscapesTheLoginInTheFilter(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{}}
	ok, err := verifierWith(fc).Verify(cfg, "*)(uid=*\\", "pw")
	if ok || err != nil {
		t.Fatalf("Verify = (%v, %v), want (false, nil)", ok, err)
	}
	if len(fc.filters) != 1 {
		t.Fatalf("searches = %d, want 1", len(fc.filters))
	}
	if want := `(mail=\2a\29\28uid=\2a\5c)`; fc.filters[0] != want {
		t.Errorf("filter = %s, want %s", fc.filters[0], want)
	}
	if len(fc.binds) != 1 || fc.binds[0] != cfg.BindDN {
		t.Errorf("binds = %v, want only service bind [%s]", fc.binds, cfg.BindDN)
	}
}

// TestVerifyStopsWhenStartTLSFails proves a refused StartTLS upgrade ends the
// session before any bind: continuing would send the service password and then
// the user's password over the plaintext connection the configuration forbids.
func TestVerifyStopsWhenStartTLSFails(t *testing.T) {
	fc := &fakeConn{startTLSErr: errors.New("StartTLS refused"), searchRes: oneEntry("uid=alice,dc=hermex,dc=test")}

	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "correct")

	if ok || err == nil {
		t.Errorf("Verify after a failed StartTLS = (%v, %v), want (false, an error)", ok, err)
	}
	if len(fc.binds) != 0 {
		t.Errorf("a password crossed the unencrypted session: binds %v", fc.binds)
	}
	if !fc.closed {
		t.Error("the connection was not closed")
	}
}

// TestStartTLSVerifiesTheDirectorysCertificate proves the upgrade checks the
// server certificate against the host the URI names (RFC 4513 section 3.1.3).
// Without that check, anyone on the path presents their own certificate and
// receives every password the verifier binds with.
func TestStartTLSVerifiesTheDirectorysCertificate(t *testing.T) {
	fc := &fakeConn{searchRes: oneEntry("uid=alice,dc=hermex,dc=test")}

	if _, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "correct"); err != nil {
		t.Fatal(err)
	}

	if len(fc.tlsConfigs) != 1 {
		t.Fatalf("StartTLS calls = %d, want 1", len(fc.tlsConfigs))
	}
	if c := fc.tlsConfigs[0]; c.InsecureSkipVerify || c.ServerName != "ad.hermex.test" {
		t.Errorf("TLS config = {ServerName: %q, InsecureSkipVerify: %v}, want the URI host with verification on", c.ServerName, c.InsecureSkipVerify)
	}
}

func verifierWith(fc *fakeConn) *Verifier {
	return &Verifier{dial: func(string) (conn, error) { fc.dialed = true; return fc, nil }}
}

func oneEntry(dn string) *ldap.SearchResult {
	return &ldap.SearchResult{Entries: []*ldap.Entry{{DN: dn}}}
}

// The fixture negotiates StartTLS: the verifier refuses a plaintext session, so
// an unencrypted configuration never reaches any of the behaviour below.
var cfg = directory.LDAPConfig{
	URI:          "ldap://ad.hermex.test:389",
	StartTLS:     true,
	BindDN:       "cn=svc,dc=hermex,dc=test",
	BindPassword: "svc",
	BaseDN:       "dc=hermex,dc=test",
	UsernameAttr: "mail",
}

// TestVerifyEmptyPasswordRejected proves an empty password is refused before any
// directory contact, an empty simple bind is an anonymous bind a server accepts.
func TestVerifyEmptyPasswordRejected(t *testing.T) {
	fc := &fakeConn{}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "")
	if ok || err != nil {
		t.Errorf("empty password = (%v, %v), want (false, nil)", ok, err)
	}
	if fc.dialed || len(fc.binds) != 0 || len(fc.filters) != 0 {
		t.Errorf("empty password must not reach the directory: dialed=%v binds=%v filters=%v", fc.dialed, fc.binds, fc.filters)
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

// TestVerifyWrongPassword proves a user bind the server refuses with
// invalidCredentials (RFC 4511 4.1.9, result code 49), which is how a directory
// answers a wrong password, is a clean false, not an error.
func TestVerifyWrongPassword(t *testing.T) {
	fc := &fakeConn{
		searchRes: oneEntry("uid=alice,dc=hermex,dc=test"),
		bindErr: map[string]error{"uid=alice,dc=hermex,dc=test": ldap.NewError(
			ldap.LDAPResultInvalidCredentials, errors.New("invalid credentials"))},
	}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "wrong")
	if ok || err != nil {
		t.Errorf("wrong password = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestVerifyReportsADirectoryFailureDuringTheBind breaks the connection during the
// user bind. That is not a verdict on the password: reporting it as a wrong one
// counts it against the account's failed-login limit and hides a directory outage
// from the operator. It must come back as an error, still with no access granted.
func TestVerifyReportsADirectoryFailureDuringTheBind(t *testing.T) {
	fc := &fakeConn{
		searchRes: oneEntry("uid=alice,dc=hermex,dc=test"),
		bindErr: map[string]error{"uid=alice,dc=hermex,dc=test": ldap.NewError(
			ldap.ErrorNetwork, errors.New("connection reset"))},
	}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "correct")
	if ok || err == nil {
		t.Errorf("bind over a broken connection = (%v, %v), want (false, an error)", ok, err)
	}
}

// TestVerifyNotFound proves a login with no directory entry does not authenticate.
func TestVerifyNotFound(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{}}
	ok, err := verifierWith(fc).Verify(cfg, "ghost@hermex.test", "x")
	if ok || err != nil {
		t.Errorf("Verify not found = (%v, %v), want (false, nil)", ok, err)
	}
	if len(fc.binds) != 1 || fc.binds[0] != cfg.BindDN {
		t.Errorf("binds = %v, want only service bind [%s]", fc.binds, cfg.BindDN)
	}
	if !fc.closed {
		t.Error("the connection was not closed")
	}
}

// TestVerifyAmbiguous proves a login matching more than one entry does not
// authenticate (it is not safe to pick one).
func TestVerifyAmbiguous(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{{DN: "a"}, {DN: "b"}}}}
	ok, err := verifierWith(fc).Verify(cfg, "dup@hermex.test", "x")
	if ok || err != nil {
		t.Errorf("Verify ambiguous = (%v, %v), want (false, nil)", ok, err)
	}
	if len(fc.binds) != 1 || fc.binds[0] != cfg.BindDN {
		t.Errorf("binds = %v, want only service bind [%s]", fc.binds, cfg.BindDN)
	}
	if !fc.closed {
		t.Error("the connection was not closed")
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
	if len(fc.filters) != 0 {
		t.Errorf("search ran after failed service bind: filters=%v", fc.filters)
	}
	if len(fc.binds) != 1 || fc.binds[0] != cfg.BindDN {
		t.Errorf("binds = %v, want only failed service bind", fc.binds)
	}
	if !fc.closed {
		t.Error("the connection was not closed")
	}
}

// dirEntry builds a directory search entry with a mail value and (optionally) a
// binary objectGUID.
func dirEntry(dn, mail string, guid []byte) *ldap.Entry {
	attrs := []*ldap.EntryAttribute{{Name: "mail", Values: []string{mail}, ByteValues: [][]byte{[]byte(mail)}}}
	if guid != nil {
		attrs = append(attrs, &ldap.EntryAttribute{Name: "objectGUID", ByteValues: [][]byte{guid}})
	}
	return &ldap.Entry{DN: dn, Attributes: attrs}
}

// TestSync proves a downsync returns each directory account's login and stable
// identifier, skipping an entry that carries no stable identifier (there is
// nothing to bind its externid to).
func TestSync(t *testing.T) {
	guid := []byte{0x01, 0x02, 0x03, 0x04}
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{
		dirEntry("uid=alice,dc=hermex,dc=test", "alice@hermex.test", guid),
		dirEntry("uid=noid,dc=hermex,dc=test", "noid@hermex.test", nil),
	}}}
	users, err := verifierWith(fc).Sync(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("Sync returned %d users, want 1 (the entry with no stable id is skipped)", len(users))
	}
	if users[0].Username != "alice@hermex.test" || !bytes.Equal(users[0].ExternID, guid) {
		t.Errorf("synced user = %+v, want alice@hermex.test with the objectGUID", users[0])
	}
}

// TestSyncProfile proves an enabled profile field set is read off each entry: a
// default-attribute field, an overridden one, and the binary photo, with the entry
// DN carried for later group-membership resolution.
func TestSyncProfile(t *testing.T) {
	guid := []byte{0x09}
	photo := []byte{0xFF, 0xD8, 0xFF} // JPEG magic bytes
	e := dirEntry("uid=alice,dc=hermex,dc=test", "alice@hermex.test", guid)
	e.Attributes = append(e.Attributes,
		&ldap.EntryAttribute{Name: "displayName", Values: []string{"Alice Adams"}},
		&ldap.EntryAttribute{Name: "jobTitle", Values: []string{"Engineer"}},
		&ldap.EntryAttribute{Name: "thumbnailPhoto", ByteValues: [][]byte{photo}},
	)
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{e}}}
	pcfg := directory.LDAPConfig{
		BaseDN:       "dc=hermex,dc=test",
		UsernameAttr: "mail",
		StartTLS:     true,
		SyncFields: map[string]directory.LDAPSyncField{
			"displayName": {Enabled: true},
			"title":       {Enabled: true, Attr: "jobTitle"},
			"photo":       {Enabled: true},
		},
	}
	users, err := verifierWith(fc).Sync(pcfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("Sync returned %d users, want 1", len(users))
	}
	u := users[0]
	if u.DN != "uid=alice,dc=hermex,dc=test" {
		t.Errorf("DN = %q, want the entry DN", u.DN)
	}
	if u.Fields["displayName"] != "Alice Adams" || u.Fields["title"] != "Engineer" {
		t.Errorf("Fields = %v, want displayName=Alice Adams, title=Engineer", u.Fields)
	}
	if !bytes.Equal(u.Photo, photo) {
		t.Errorf("Photo = %v, want the thumbnailPhoto bytes", u.Photo)
	}
}

// TestSyncGroups proves group sync returns each mail-bearing group's address, owner
// DN, and member DNs, and is a no-op (no connection) when disabled.
func TestSyncGroups(t *testing.T) {
	g := &ldap.Entry{DN: "cn=eng,dc=hermex,dc=test", Attributes: []*ldap.EntryAttribute{
		{Name: "mail", Values: []string{"eng@hermex.test"}},
		{Name: "managedBy", Values: []string{"uid=alice,dc=hermex,dc=test"}},
		{Name: "member", Values: []string{"uid=bob,dc=hermex,dc=test", "uid=carol,dc=hermex,dc=test"}},
	}}
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{g}}}
	cfg := directory.LDAPConfig{BaseDN: "dc=hermex,dc=test", UsernameAttr: "mail", SyncGroups: true, StartTLS: true}
	groups, err := verifierWith(fc).SyncGroups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("SyncGroups returned %d, want 1", len(groups))
	}
	if groups[0].Mail != "eng@hermex.test" || groups[0].OwnerDN != "uid=alice,dc=hermex,dc=test" || len(groups[0].MemberDNs) != 2 {
		t.Errorf("group = %+v, want eng with owner alice and 2 members", groups[0])
	}

	fcDisabled := &fakeConn{}
	got, err := verifierWith(fcDisabled).SyncGroups(directory.LDAPConfig{})
	if err != nil {
		t.Fatalf("SyncGroups disabled: %v", err)
	}
	if got != nil {
		t.Errorf("SyncGroups disabled = %v, want nil (no-op)", got)
	}
	if fcDisabled.dialed {
		t.Error("SyncGroups disabled must not dial the directory")
	}
}

// aliasEntry builds a user entry carrying the given raw alias attribute values.
func aliasEntry(values ...string) *ldap.Entry {
	e := dirEntry("uid=alice,dc=hermex,dc=test", "alice@hermex.test", []byte{0x01})
	e.Attributes = append(e.Attributes, &ldap.EntryAttribute{Name: "proxyAddresses", Values: values})
	return e
}

// aliasCfg is a configuration that reads aliases from proxyAddresses.
var aliasCfg = directory.LDAPConfig{
	BaseDN: "dc=hermex,dc=test", UsernameAttr: "mail", StartTLS: true, AliasAttr: "proxyAddresses",
}

// syncedAliases runs one downsync over an entry and returns the account's aliases.
func syncedAliases(t *testing.T, cfg directory.LDAPConfig, e *ldap.Entry) []string {
	t.Helper()
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{e}}}
	users, err := verifierWith(fc).Sync(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("Sync returned %d users, want 1", len(users))
	}
	return users[0].Aliases
}

// TestSyncStripsTheSMTPPrefix proves a secondary proxy address reaches the account as a
// plain lower-cased address.
func TestSyncStripsTheSMTPPrefix(t *testing.T) {
	got := syncedAliases(t, aliasCfg, aliasEntry("smtp:Sales@Hermex.Test"))

	if len(got) != 1 || got[0] != "sales@hermex.test" {
		t.Errorf("aliases = %v, want [sales@hermex.test] lower-cased", got)
	}
}

// TestSyncAcceptsTheUppercasePrefix proves the primary proxy address, which Active Directory
// marks with an uppercase SMTP:, is taken as an alias too when it differs from the login.
func TestSyncAcceptsTheUppercasePrefix(t *testing.T) {
	got := syncedAliases(t, aliasCfg, aliasEntry("SMTP:office@hermex.test"))

	if len(got) != 1 || got[0] != "office@hermex.test" {
		t.Errorf("aliases = %v, want [office@hermex.test]", got)
	}
}

// TestSyncDropsNonMailSchemes is the load-bearing filter: a directory publishes x500 and sip
// addresses in the same attribute, and neither is a mail address.
func TestSyncDropsNonMailSchemes(t *testing.T) {
	got := syncedAliases(t, aliasCfg, aliasEntry("x500:/o=org/cn=alice", "sip:alice@hermex.test"))

	if len(got) != 0 {
		t.Errorf("aliases = %v, want none: only smtp values are addresses", got)
	}
}

// TestSyncDropsTheAccountsOwnAddress proves the login is not synced back as an alias of
// itself, which the alias checks would refuse anyway.
func TestSyncDropsTheAccountsOwnAddress(t *testing.T) {
	got := syncedAliases(t, aliasCfg, aliasEntry("SMTP:Alice@Hermex.Test"))

	if len(got) != 0 {
		t.Errorf("aliases = %v, want none: that is the account's own address", got)
	}
}

// TestSyncReadsNoAliasesWhenUnconfigured proves an empty alias attribute leaves the account's
// aliases untouched, because the downsync reports none to apply.
func TestSyncReadsNoAliasesWhenUnconfigured(t *testing.T) {
	got := syncedAliases(t, cfg, aliasEntry("smtp:sales@hermex.test"))

	if len(got) != 0 {
		t.Errorf("aliases = %v, want none while no alias attribute is configured", got)
	}
}

// contactEntry builds a mail contact search entry with an address, a display name and
// (optionally) a binary objectGUID.
func contactEntry(dn, mail, displayName string, guid []byte) *ldap.Entry {
	attrs := []*ldap.EntryAttribute{
		{Name: "mail", Values: []string{mail}, ByteValues: [][]byte{[]byte(mail)}},
		{Name: "displayName", Values: []string{displayName}},
	}
	if guid != nil {
		attrs = append(attrs, &ldap.EntryAttribute{Name: "objectGUID", ByteValues: [][]byte{guid}})
	}
	return &ldap.Entry{DN: dn, Attributes: attrs}
}

// contactCfg is a configuration with contact sync enabled.
var contactCfg = directory.LDAPConfig{BaseDN: "dc=hermex,dc=test", UsernameAttr: "mail", SyncContacts: true, StartTLS: true}

// TestSyncContacts proves contact sync returns each mail-bearing contact's address,
// display name and stable identifier.
func TestSyncContacts(t *testing.T) {
	guid := []byte{0x11, 0x22}
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{
		contactEntry("cn=partner,dc=hermex,dc=test", "partner@remote.test", "Partner Inc", guid),
	}}}

	contacts, err := verifierWith(fc).SyncContacts(contactCfg)
	if err != nil {
		t.Fatal(err)
	}

	if len(contacts) != 1 {
		t.Fatalf("SyncContacts returned %d, want 1", len(contacts))
	}
	if contacts[0].Mail != "partner@remote.test" || contacts[0].DisplayName != "Partner Inc" || !bytes.Equal(contacts[0].ExternID, guid) {
		t.Errorf("contact = %+v, want partner@remote.test / Partner Inc / the objectGUID", contacts[0])
	}
}

// TestSyncContactsSkipsAnEntryWithNoIdentifier proves an entry with no stable identifier
// is skipped: there is nothing to bind its externid to, so a later sync could not match it
// again.
func TestSyncContactsSkipsAnEntryWithNoIdentifier(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{
		contactEntry("cn=noid,dc=hermex,dc=test", "noid@remote.test", "No Id", nil),
	}}}

	contacts, err := verifierWith(fc).SyncContacts(contactCfg)
	if err != nil {
		t.Fatalf("SyncContacts: %v", err)
	}

	if len(contacts) != 0 {
		t.Errorf("contacts = %+v, want none: the entry carries no stable identifier", contacts)
	}
}

// TestSyncContactsFallsBackToCN proves a contact with no displayName is named by its cn,
// so it does not land in the address book as a bare address.
func TestSyncContactsFallsBackToCN(t *testing.T) {
	e := &ldap.Entry{DN: "cn=partner,dc=hermex,dc=test", Attributes: []*ldap.EntryAttribute{
		{Name: "mail", Values: []string{"partner@remote.test"}},
		{Name: "cn", Values: []string{"Partner Inc"}},
		{Name: "objectGUID", ByteValues: [][]byte{{0x33}}},
	}}
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{e}}}

	contacts, err := verifierWith(fc).SyncContacts(contactCfg)
	if err != nil {
		t.Fatalf("SyncContacts: %v", err)
	}

	if len(contacts) != 1 || contacts[0].DisplayName != "Partner Inc" {
		t.Errorf("contacts = %+v, want the cn as the display name", contacts)
	}
}

// TestSyncContactsDisabledIsANoOp proves contact sync opens no connection while it is off.
func TestSyncContactsDisabledIsANoOp(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{}}

	got, err := verifierWith(fc).SyncContacts(directory.LDAPConfig{})
	if err != nil {
		t.Fatalf("SyncContacts disabled: %v", err)
	}
	if got != nil {
		t.Errorf("SyncContacts disabled = %v, want nil (no-op)", got)
	}
	if fc.dialed {
		t.Error("SyncContacts disabled must not dial the directory")
	}
}

// TestVerifyRefusesPlaintextTransport proves a configuration that would bind in
// the clear never reaches the directory. Verifying a password means binding as
// the user, so a plain session would put every LDAP-mastered account's real
// mailbox password on the network.
func TestVerifyRefusesPlaintextTransport(t *testing.T) {
	fc := &fakeConn{searchRes: oneEntry("uid=alice,dc=hermex,dc=test")}
	plain := cfg
	plain.StartTLS = false

	ok, err := verifierWith(fc).Verify(plain, "alice@hermex.test", "correct")
	if ok {
		t.Error("a plaintext configuration authenticated a login")
	}
	if !errors.Is(err, directory.ErrInsecureLDAP) {
		t.Errorf("err = %v, want directory.ErrInsecureLDAP", err)
	}
	if fc.dialed || len(fc.binds) != 0 {
		t.Errorf("the password reached the directory anyway: dialed=%v binds=%v", fc.dialed, fc.binds)
	}
}

// TestVerifyAcceptsLDAPS proves the other secure shape works: an ldaps:// URI
// with no StartTLS upgrade.
func TestVerifyAcceptsLDAPS(t *testing.T) {
	fc := &fakeConn{searchRes: oneEntry("uid=alice,dc=hermex,dc=test")}
	secure := cfg
	secure.URI, secure.StartTLS = "ldaps://ad.hermex.test:636", false

	if ok, err := verifierWith(fc).Verify(secure, "alice@hermex.test", "correct"); !ok || err != nil {
		t.Errorf("Verify over ldaps = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestAFailedCloseIsReported: a connection that will not close is a directory
// error, so Verify does not answer true over it and every sync reports it
// instead of returning a result as if the session ended cleanly.
func TestAFailedCloseIsReported(t *testing.T) {
	closeErr := errors.New("unbind failed")
	fc := &fakeConn{searchRes: oneEntry("uid=alice,dc=hermex,dc=test"), closeErr: closeErr}
	if ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "correct"); ok || !errors.Is(err, closeErr) {
		t.Errorf("Verify with a failed close = (%v, %v), want (false, the close error)", ok, err)
	}
	fc = &fakeConn{searchRes: &ldap.SearchResult{}, closeErr: closeErr}
	if _, err := verifierWith(fc).Sync(cfg); !errors.Is(err, closeErr) {
		t.Errorf("Sync with a failed close = %v, want the close error", err)
	}
	fc = &fakeConn{searchRes: &ldap.SearchResult{}, closeErr: closeErr}
	if _, err := verifierWith(fc).SyncGroups(directory.LDAPConfig{BaseDN: "dc=hermex,dc=test", SyncGroups: true, StartTLS: true}); !errors.Is(err, closeErr) {
		t.Errorf("SyncGroups with a failed close = %v, want the close error", err)
	}
	fc = &fakeConn{searchRes: &ldap.SearchResult{}, closeErr: closeErr}
	if _, err := verifierWith(fc).SyncContacts(contactCfg); !errors.Is(err, closeErr) {
		t.Errorf("SyncContacts with a failed close = %v, want the close error", err)
	}
}

// TestVerifyEmptyURIRejected proves a missing directory URI is refused before
// dialing or binding.
func TestVerifyEmptyURIRejected(t *testing.T) {
	fc := &fakeConn{}
	noURI := cfg
	noURI.URI = ""

	ok, err := verifierWith(fc).Verify(noURI, "alice@hermex.test", "secret")
	if ok || err != nil {
		t.Errorf("empty URI = (%v, %v), want (false, nil)", ok, err)
	}
	if fc.dialed || len(fc.binds) != 0 || len(fc.filters) != 0 {
		t.Errorf("empty URI must not contact directory: dialed=%v binds=%v filters=%v", fc.dialed, fc.binds, fc.filters)
	}
}

// TestVerifyMalformedURIWithStartTLSRejected proves a malformed URI that fails
// host extraction aborts StartTLS before negotiating TLS with an empty
// ServerName (RFC 4513 section 3.1.3) and closes the connection.
func TestVerifyMalformedURIWithStartTLSRejected(t *testing.T) {
	fc := &fakeConn{searchRes: oneEntry("uid=alice,dc=hermex,dc=test")}
	bad := cfg
	bad.URI = "ldap://[::1"

	ok, err := verifierWith(fc).Verify(bad, "alice@hermex.test", "secret")
	if ok || err == nil {
		t.Fatalf("malformed URI with StartTLS = (%v, %v), want (false, error)", ok, err)
	}
	if len(fc.tlsConfigs) != 0 {
		t.Errorf("StartTLS was called despite malformed URI: %v", fc.tlsConfigs)
	}
	if len(fc.binds) != 0 {
		t.Errorf("binds occurred despite malformed URI: %v", fc.binds)
	}
	if !fc.closed {
		t.Error("the connection was not closed after URI parse failure")
	}
}

// TestVerifyEscapesNULInFilter proves a NUL byte in the login is escaped as
// \00 per RFC 4515 section 3 and never triggers a user bind on no match.
func TestVerifyEscapesNULInFilter(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{}}
	ok, err := verifierWith(fc).Verify(cfg, "alice\x00admin", "pw")
	if ok || err != nil {
		t.Fatalf("Verify = (%v, %v), want (false, nil)", ok, err)
	}
	if len(fc.filters) != 1 {
		t.Fatalf("searches = %d, want 1", len(fc.filters))
	}
	if want := `(mail=alice\00admin)`; fc.filters[0] != want {
		t.Errorf("filter = %s, want %s", fc.filters[0], want)
	}
	if len(fc.binds) != 1 || fc.binds[0] != cfg.BindDN {
		t.Errorf("binds = %v, want only service bind [%s]", fc.binds, cfg.BindDN)
	}
}

// TestVerifySearchErrorRefusesLogin proves a directory search failure returns
// an error, never attempts a user bind, and closes the connection.
func TestVerifySearchErrorRefusesLogin(t *testing.T) {
	fc := &fakeConn{searchErr: errors.New("search timed out")}
	ok, err := verifierWith(fc).Verify(cfg, "alice@hermex.test", "secret")
	if ok || err == nil {
		t.Fatalf("Verify on search error = (%v, %v), want (false, error)", ok, err)
	}
	if len(fc.binds) != 1 || fc.binds[0] != cfg.BindDN {
		t.Errorf("binds = %v, want only service bind [%s]", fc.binds, cfg.BindDN)
	}
	if !fc.closed {
		t.Error("the connection was not closed")
	}
}

// TestSyncRefusesPlaintextTransport proves Sync refuses a plaintext LDAP
// configuration before dialing and returns no accounts.
func TestSyncRefusesPlaintextTransport(t *testing.T) {
	fc := &fakeConn{searchRes: &ldap.SearchResult{}}
	plain := cfg
	plain.StartTLS = false

	users, err := verifierWith(fc).Sync(plain)
	if !errors.Is(err, directory.ErrInsecureLDAP) {
		t.Errorf("err = %v, want directory.ErrInsecureLDAP", err)
	}
	if users != nil {
		t.Errorf("users = %+v, want nil", users)
	}
	if fc.dialed || len(fc.binds) != 0 {
		t.Errorf("plaintext Sync contacted directory: dialed=%v binds=%v", fc.dialed, fc.binds)
	}
}

// TestSyncSkipsMissingLoginAndEmptyAliasValues proves entries with an empty
// login attribute are skipped and empty smtp: alias values are dropped.
func TestSyncSkipsMissingLoginAndEmptyAliasValues(t *testing.T) {
	noLogin := dirEntry("uid=ghost,dc=hermex,dc=test", "", []byte{0x02})
	emptyAlias := aliasEntry("smtp:", "SMTP:", "   ", "")
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{noLogin, emptyAlias}}}

	users, err := verifierWith(fc).Sync(aliasCfg)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("Sync returned %d users, want 1 (empty login entry skipped)", len(users))
	}
	if users[0].Username != "alice@hermex.test" {
		t.Errorf("Username = %q, want alice@hermex.test", users[0].Username)
	}
	if len(users[0].Aliases) != 0 {
		t.Errorf("Aliases = %v, want none for empty smtp: and blank values", users[0].Aliases)
	}
}

// TestSyncGroupsSkipsEntryWithoutMail proves a group entry lacking a mail
// attribute is skipped because a distribution list requires an address.
func TestSyncGroupsSkipsEntryWithoutMail(t *testing.T) {
	noMail := &ldap.Entry{DN: "cn=nomail,dc=hermex,dc=test", Attributes: []*ldap.EntryAttribute{
		{Name: "member", Values: []string{"uid=bob,dc=hermex,dc=test"}},
	}}
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{noMail}}}
	gcfg := directory.LDAPConfig{BaseDN: "dc=hermex,dc=test", UsernameAttr: "mail", SyncGroups: true, StartTLS: true}

	groups, err := verifierWith(fc).SyncGroups(gcfg)
	if err != nil {
		t.Fatalf("SyncGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("groups = %+v, want none for entry without mail", groups)
	}
}

// TestSyncContactsSkipsEntryWithoutMail proves a contact entry lacking a mail
// attribute is skipped even when it carries a stable identifier.
func TestSyncContactsSkipsEntryWithoutMail(t *testing.T) {
	noMail := contactEntry("cn=nomail,dc=hermex,dc=test", "", "No Mail", []byte{0x44})
	fc := &fakeConn{searchRes: &ldap.SearchResult{Entries: []*ldap.Entry{noMail}}}

	contacts, err := verifierWith(fc).SyncContacts(contactCfg)
	if err != nil {
		t.Fatalf("SyncContacts: %v", err)
	}
	if len(contacts) != 0 {
		t.Errorf("contacts = %+v, want none for entry without mail", contacts)
	}
}
