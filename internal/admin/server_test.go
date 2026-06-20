package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/activesync"
	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// fakeDir is a scripted Directory for the admin server tests.
type fakeDir struct {
	authOK  bool
	uid     int64
	roles   []directory.AdminRole
	domains []directory.DomainInfo
	users   []directory.UserInfo
	aliases []directory.AliasInfo
	ldap    map[int64]directory.LDAPConfig

	// captured by the create handlers
	createdDomain, createdHomedir string
	createdUser, createdMaildir   string
	createdAlias, createdAliasTo  string
	setPwUser, setPwValue         string
	setPwMissing                  bool
	grantedRole, revokedRole      string
	grantedScope, revokedScope    int64
	upsertedUsers                 []string
	upsertNew                     bool
	createErr                     error

	// captured/scripted by the user detail/edit/delete handlers
	userDetail           directory.UserDetail
	getUserMissing       bool
	gotUser, updatedUser string
	updateUser           directory.UserUpdate
	updateMissing        bool
	deletedUser          string
	deleteFiles          bool
	deleteMissing        bool

	altnames        []string
	setAltnames     []string
	setAltnamesUser string
	altnamesMissing bool

	userAliases    []string
	setAliases     []string
	setAliasesUser string
	aliasesMissing bool

	forward        directory.ForwardInfo
	forwardSet     bool
	setForwardUser string
	setForwardType int
	setForwardDest string
	forwardMissing bool

	userProps       map[uint32]string
	setProps        map[uint32]string
	setPropsUser    string
	setPropsMissing bool

	mlists             []directory.MListInfo
	createdMList       string
	createdMListType   int
	createdMListPriv   int
	deletedMList       string
	deleteMListMissing bool
	mlistMembers       []string
	setMembersUser     string
	setMembers         []string
	mlistSpecifieds    []string
	setSpecifiedsUser  string
	setSpecifieds      []string
	mlistMissing       bool

	contacts             []directory.ContactInfo
	createdContact       string
	createdContactName   string
	createdContactDomain string
	updatedContact       string
	updatedContactName   string
	updateContactMissing bool
	deletedContact       string
	deleteContactMissing bool
}

func (f *fakeDir) Authenticate(_, _ string) (string, bool) {
	if f.authOK {
		return "/mbox", true
	}
	return "", false
}
func (f *fakeDir) UserID(_ string) (int64, bool, error)            { return f.uid, f.uid != 0, nil }
func (f *fakeDir) AdminRoles(int64) ([]directory.AdminRole, error) { return f.roles, nil }
func (f *fakeDir) GrantAdminRole(_ int64, role string, scopeID int64) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.grantedRole, f.grantedScope = role, scopeID
	return nil
}
func (f *fakeDir) RevokeAdminRole(_ int64, role string, scopeID int64) error {
	f.revokedRole, f.revokedScope = role, scopeID
	return nil
}
func (f *fakeDir) ListDomains() ([]directory.DomainInfo, error) { return f.domains, nil }
func (f *fakeDir) ListUsers() ([]directory.UserInfo, error)     { return f.users, nil }
func (f *fakeDir) CreateDomain(name, homedir string) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.createdDomain, f.createdHomedir = name, homedir
	return 42, nil
}
func (f *fakeDir) CreateUser(username, _, maildir string) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.createdUser, f.createdMaildir = username, maildir
	return 43, nil
}
func (f *fakeDir) SetPassword(username, password string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setPwUser, f.setPwValue = username, password
	return !f.setPwMissing, nil
}
func (f *fakeDir) ListAliases() ([]directory.AliasInfo, error) { return f.aliases, nil }
func (f *fakeDir) CreateAlias(aliasname, mainname string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createdAlias, f.createdAliasTo = aliasname, mainname
	return nil
}
func (f *fakeDir) GetLDAPConfig(orgID int64) (directory.LDAPConfig, bool, error) {
	c, ok := f.ldap[orgID]
	return c, ok, nil
}
func (f *fakeDir) SetLDAPConfig(orgID int64, cfg directory.LDAPConfig) error {
	if f.ldap == nil {
		f.ldap = map[int64]directory.LDAPConfig{}
	}
	f.ldap[orgID] = cfg
	return nil
}
func (f *fakeDir) UpsertLDAPUser(username string, _ []byte, _ string) (bool, error) {
	f.upsertedUsers = append(f.upsertedUsers, username)
	return f.upsertNew, nil
}
func (f *fakeDir) GetUser(username string) (directory.UserDetail, bool, error) {
	f.gotUser = username
	if f.getUserMissing {
		return directory.UserDetail{}, false, nil
	}
	return f.userDetail, true, nil
}
func (f *fakeDir) UpdateUser(username string, u directory.UserUpdate) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.updatedUser, f.updateUser = username, u
	return !f.updateMissing, nil
}
func (f *fakeDir) DeleteUser(username string, deleteFiles bool) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.deletedUser, f.deleteFiles = username, deleteFiles
	return !f.deleteMissing, nil
}
func (f *fakeDir) ListAltnames(string) ([]string, error) { return f.altnames, nil }
func (f *fakeDir) SetAltnames(username string, altnames []string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setAltnamesUser, f.setAltnames = username, altnames
	return !f.altnamesMissing, nil
}
func (f *fakeDir) ListAliasesFor(string) ([]string, error) { return f.userAliases, nil }
func (f *fakeDir) SetAliasesFor(username string, aliases []string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setAliasesUser, f.setAliases = username, aliases
	return !f.aliasesMissing, nil
}
func (f *fakeDir) GetForward(string) (directory.ForwardInfo, bool, error) {
	return f.forward, f.forwardSet, nil
}
func (f *fakeDir) SetForward(username string, forwardType int, destination string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setForwardUser, f.setForwardType, f.setForwardDest = username, forwardType, destination
	return !f.forwardMissing, nil
}
func (f *fakeDir) ListMLists() ([]directory.MListInfo, error) { return f.mlists, nil }
func (f *fakeDir) CreateMList(listname string, listType, listPriv int) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.createdMList, f.createdMListType, f.createdMListPriv = listname, listType, listPriv
	return 1, nil
}
func (f *fakeDir) DeleteMList(listname string) (bool, error) {
	f.deletedMList = listname
	return !f.deleteMListMissing, nil
}
func (f *fakeDir) ListMembers(string) ([]string, error) { return f.mlistMembers, nil }
func (f *fakeDir) SetMembers(listname string, members []string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setMembersUser, f.setMembers = listname, members
	return !f.mlistMissing, nil
}
func (f *fakeDir) ListSpecifieds(string) ([]string, error) { return f.mlistSpecifieds, nil }
func (f *fakeDir) SetSpecifieds(listname string, senders []string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setSpecifiedsUser, f.setSpecifieds = listname, senders
	return !f.mlistMissing, nil
}
func (f *fakeDir) ListContacts() ([]directory.ContactInfo, error) { return f.contacts, nil }
func (f *fakeDir) CreateContact(email, displayName, domain string) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.createdContact, f.createdContactName, f.createdContactDomain = email, displayName, domain
	return 1, nil
}
func (f *fakeDir) UpdateContact(email, displayName string) (bool, error) {
	f.updatedContact, f.updatedContactName = email, displayName
	return !f.updateContactMissing, nil
}
func (f *fakeDir) DeleteContact(email string) (bool, error) {
	f.deletedContact = email
	return !f.deleteContactMissing, nil
}
func (f *fakeDir) GetUserProperties(string) (map[uint32]string, error) { return f.userProps, nil }
func (f *fakeDir) SetUserProperties(username string, props map[uint32]string) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.setPropsUser, f.setProps = username, props
	return !f.setPropsMissing, nil
}

// fakePaths derives resource paths under a fixed root for the tests.
type fakePaths struct{ root string }

func (p fakePaths) HomedirFor(domain string) string  { return p.root + "/dom/" + domain }
func (p fakePaths) MaildirFor(address string) string { return p.root + "/mbox/" + address }

// fakeStore is a scripted MailboxStore for the admin store-backed tabs: it holds
// the out-of-office settings and the device list keyed by maildir, and captures
// the last write or device action.
type fakeStore struct {
	oof    map[string]objectstore.OOFSettings
	setDir string
	setOOF objectstore.OOFSettings
	getErr error
	setErr error

	devices         map[string][]activesync.DeviceInfo
	deviceAction    string // "resync"/"delete"/"wipe"/"wipe-account"/"cancel"
	deviceActionDir string
	deviceActionID  string

	quota       map[string]objectstore.QuotaLimits
	used        map[string]int64
	setQuotaDir string
	setQuotaVal objectstore.QuotaLimits

	delegates       map[string][]string
	setDelegatesDir string
	setDelegatesVal []string
}

func (f *fakeStore) GetOOFSettings(maildir string) (objectstore.OOFSettings, error) {
	if f.getErr != nil {
		return objectstore.OOFSettings{}, f.getErr
	}
	return f.oof[maildir], nil
}

func (f *fakeStore) SetOOFSettings(maildir string, cfg objectstore.OOFSettings) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.oof == nil {
		f.oof = map[string]objectstore.OOFSettings{}
	}
	f.oof[maildir] = cfg
	f.setDir, f.setOOF = maildir, cfg
	return nil
}

func (f *fakeStore) ListDevices(maildir string) ([]activesync.DeviceInfo, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.devices[maildir], nil
}

func (f *fakeStore) ResyncDevice(maildir, deviceID string) error {
	return f.recordDeviceAction("resync", maildir, deviceID)
}

func (f *fakeStore) DeleteDevice(maildir, deviceID string) error {
	return f.recordDeviceAction("delete", maildir, deviceID)
}

func (f *fakeStore) WipeDevice(maildir, deviceID string, accountOnly bool) error {
	action := "wipe"
	if accountOnly {
		action = "wipe-account"
	}
	return f.recordDeviceAction(action, maildir, deviceID)
}

func (f *fakeStore) CancelDeviceWipe(maildir, deviceID string) error {
	return f.recordDeviceAction("cancel", maildir, deviceID)
}

// recordDeviceAction captures the last per-device action for assertions.
func (f *fakeStore) recordDeviceAction(action, maildir, deviceID string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.deviceAction, f.deviceActionDir, f.deviceActionID = action, maildir, deviceID
	return nil
}

func (f *fakeStore) GetQuota(maildir string) (objectstore.QuotaLimits, int64, error) {
	if f.getErr != nil {
		return objectstore.QuotaLimits{}, 0, f.getErr
	}
	return f.quota[maildir], f.used[maildir], nil
}

func (f *fakeStore) SetQuota(maildir string, q objectstore.QuotaLimits) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.setQuotaDir, f.setQuotaVal = maildir, q
	return nil
}

func (f *fakeStore) GetDelegates(maildir string) ([]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.delegates[maildir], nil
}

func (f *fakeStore) SetDelegates(maildir string, list []string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.delegates == nil {
		f.delegates = map[string][]string{}
	}
	f.delegates[maildir] = list
	f.setDelegatesDir, f.setDelegatesVal = maildir, list
	return nil
}

// adminServer builds a test server with an empty fake mailbox store, so no test
// ever opens a real object store. OOF tests that inspect the store use
// adminServerStore.
func adminServer(t *testing.T, d Directory) *httptest.Server {
	return adminServerStore(t, d, &fakeStore{})
}

// adminServerStore builds a test server whose store-backed tabs are served by the
// given MailboxStore, which the caller keeps a handle to for assertions.
func adminServerStore(t *testing.T, d Directory, store MailboxStore) *httptest.Server {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = store
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func login(t *testing.T, ts *httptest.Server) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/admin/login", "application/json",
		strings.NewReader(`{"login":"admin@hermex.test","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	return resp, resp.Header.Get("Set-Cookie")
}

// cookieValue extracts a named cookie's value from a Set-Cookie header (the
// Secure cookies would otherwise not ride back over httptest's plain HTTP).
func cookieValue(setCookie, name string) string {
	return strings.SplitN(strings.TrimPrefix(setCookie, name+"="), ";", 2)[0]
}

// loginCookies logs in and returns the session and CSRF cookie values.
func loginCookies(t *testing.T, ts *httptest.Server) (session, csrf string) {
	t.Helper()
	resp, _ := login(t, ts)
	resp.Body.Close()
	for _, sc := range resp.Header["Set-Cookie"] {
		if strings.HasPrefix(sc, sessionCookie+"=") {
			session = cookieValue(sc, sessionCookie)
		}
		if strings.HasPrefix(sc, csrfCookie+"=") {
			csrf = cookieValue(sc, csrfCookie)
		}
	}
	return session, csrf
}

// TestAdminLoginAndWhoami proves a valid admin login sets a session and whoami
// reports the admin's identity and roles.
func TestAdminLoginAndWhoami(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)

	resp, setCookie := login(t, ts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(setCookie, sessionCookie+"=") {
		t.Fatalf("login set no session cookie: %q", setCookie)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/admin/whoami", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookieValue(setCookie, sessionCookie)})
	who, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer who.Body.Close()
	if who.StatusCode != http.StatusOK {
		t.Fatalf("whoami status %d, want 200", who.StatusCode)
	}
	body, _ := io.ReadAll(who.Body)
	if !strings.Contains(string(body), "admin@hermex.test") || !strings.Contains(string(body), "system") {
		t.Errorf("whoami body = %s, want the login and the system role", body)
	}
}

// TestAdminLoginBadCreds proves wrong credentials are refused.
func TestAdminLoginBadCreds(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: false})
	resp, _ := login(t, ts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad-credentials status %d, want 401", resp.StatusCode)
	}
}

// TestAdminLoginNonAdmin proves a user who authenticates but holds no admin role
// is refused with 401 — the same status as wrong credentials, so a valid
// non-admin login is not an oracle confirming the password was correct.
func TestAdminLoginNonAdmin(t *testing.T) {
	ts := adminServer(t, &fakeDir{authOK: true, uid: 7, roles: nil})
	resp, _ := login(t, ts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("non-admin status %d, want 401", resp.StatusCode)
	}
}

// TestAdminCSRF proves a state-changing request needs a matching CSRF token: a
// logout with the session but no CSRF header is refused, and one carrying the
// header succeeds.
func TestAdminCSRF(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	if session == "" || csrf == "" {
		t.Fatalf("login set session=%q csrf=%q, want both", session, csrf)
	}

	withCookies := func(setHeader bool) int {
		req, _ := http.NewRequest("POST", ts.URL+"/admin/logout", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
		if setHeader {
			req.Header.Set(csrfHeader, csrf)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := withCookies(false); code != http.StatusForbidden {
		t.Errorf("logout without CSRF header = %d, want 403", code)
	}
	if code := withCookies(true); code != http.StatusNoContent {
		t.Errorf("logout with CSRF header = %d, want 204", code)
	}
}

// TestAdminWhoamiNoSession proves a protected endpoint refuses an unauthenticated
// request.
func TestAdminWhoamiNoSession(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	resp, err := http.Get(ts.URL + "/admin/whoami")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-session whoami status %d, want 401", resp.StatusCode)
	}
}
