package admin

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/activesync"
	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/objectstore"
)

// fakeDir is a scripted Directory for the admin server tests.
type fakeDir struct {
	authOK            bool
	uid               int64
	roles             []directory.AdminRole
	perms             []directory.Permission
	domains           []directory.DomainInfo
	users             []directory.UserInfo
	aliases           []directory.AliasInfo
	maildirs          []string
	verdicts          []directory.SpamVerdict
	ldap              map[int64]directory.LDAPConfig
	defaultSyncPolicy easpolicy.Policy

	// domain device-policy override: GetDomainSyncPolicy returns domainSyncPolicy;
	// SetDomainSyncPolicy records it and the domain (domainSyncPolicyMissing => not found)
	domainSyncPolicy          easpolicy.Policy
	setDomainSyncPolicyDomain string
	domainSyncPolicyMissing   bool

	// create-defaults by scope (0 = system, domain id = override); set in a test
	createDefaults             map[int64]directory.CreateDefaults
	effectiveUserDefaults      directory.ResolvedUserDefaults
	setCreateDefaultsScope     int64
	deletedCreateDefaultsScope int64
	activeSessions             []directory.SessionRecord
	fetchmail                  map[string][]directory.FetchmailEntry
	nextFMID                   int64
	orgs                       map[int64]directory.OrgInfo
	nextOrgID                  int64
	namedRoles                 map[int64]directory.RoleDetail
	nextRoleID                 int64

	// captured by PurgeDomain; purgeDomainMissing makes it report ok=false
	purgedDomain       int64
	purgeFiles         bool
	purgeDomainMissing bool

	// GetDomain returns domainDetail (getDomainMissing => not found); UpdateDomain
	// records its id and argument (updateDomainMissing => not found)
	domainDetail        directory.DomainDetail
	getDomainMissing    bool
	updatedDomain       int64
	updateDomainArg     directory.DomainUpdate
	updateDomainMissing bool

	// captured by AssignDomainToOrg; assignDomainMissing makes it report ok=false
	assignDomainID, assignOrgID int64
	assignDomainMissing         bool

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
	knownUsers           map[string]directory.UserDetail // when set, GetUser resolves per-name
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
	tasks                []directory.TaskInfo
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

// EffectivePermissions mirrors the real resolver's union bridge: any explicitly
// scripted named-role permissions in f.perms, plus the equivalents of the tier
// grants in f.roles. Tests script f.roles for the legacy path and f.perms to
// exercise the read-only and capability permissions.
func (f *fakeDir) EffectivePermissions(int64) ([]directory.Permission, error) {
	seen := map[directory.Permission]bool{}
	var out []directory.Permission
	add := func(p directory.Permission) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range f.perms {
		add(p)
	}
	for _, r := range f.roles {
		switch r.Role {
		case directory.AdminSystem:
			add(directory.Permission{Name: directory.PermSystemAdmin})
		case directory.AdminOrg:
			add(directory.Permission{Name: directory.PermOrgAdmin, Params: strconv.FormatInt(r.ScopeID, 10)})
		case directory.AdminDomain:
			add(directory.Permission{Name: directory.PermDomainAdmin, Params: strconv.FormatInt(r.ScopeID, 10)})
		}
	}
	return out, nil
}

func (f *fakeDir) ListRoles() ([]directory.RoleInfo, error) {
	out := make([]directory.RoleInfo, 0, len(f.namedRoles))
	for _, r := range f.namedRoles {
		out = append(out, r.RoleInfo)
	}
	return out, nil
}

func (f *fakeDir) GetRole(id int64) (directory.RoleDetail, bool, error) {
	r, ok := f.namedRoles[id]
	return r, ok, nil
}

func (f *fakeDir) CreateRole(name, description string, perms []directory.Permission, userIDs []int64) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	if f.namedRoles == nil {
		f.namedRoles = map[int64]directory.RoleDetail{}
	}
	f.nextRoleID++
	f.namedRoles[f.nextRoleID] = roleDetail(f.nextRoleID, name, description, perms, userIDs)
	return f.nextRoleID, nil
}

func (f *fakeDir) UpdateRole(id int64, name, description string, perms []directory.Permission, userIDs []int64) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	if _, ok := f.namedRoles[id]; !ok {
		return false, nil
	}
	f.namedRoles[id] = roleDetail(id, name, description, perms, userIDs)
	return true, nil
}

func (f *fakeDir) DeleteRole(id int64) (bool, error) {
	if _, ok := f.namedRoles[id]; !ok {
		return false, nil
	}
	delete(f.namedRoles, id)
	return true, nil
}

// roleDetail assembles a RoleDetail for the fake, mirroring the real store's
// derived counts.
func roleDetail(id int64, name, description string, perms []directory.Permission, userIDs []int64) directory.RoleDetail {
	return directory.RoleDetail{
		RoleInfo: directory.RoleInfo{
			ID: id, Name: name, Description: description,
			PermCount: len(perms), UserCount: len(userIDs),
		},
		Permissions: perms,
		UserIDs:     userIDs,
	}
}
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

func (f *fakeDir) PurgeDomain(domainID int64, deleteFiles bool) (bool, error) {
	f.purgedDomain, f.purgeFiles = domainID, deleteFiles
	return !f.purgeDomainMissing, nil
}
func (f *fakeDir) GetDomain(id int64) (directory.DomainDetail, bool, error) {
	if f.getDomainMissing {
		return directory.DomainDetail{}, false, nil
	}
	return f.domainDetail, true, nil
}
func (f *fakeDir) UpdateDomain(id int64, u directory.DomainUpdate) (bool, error) {
	if f.createErr != nil {
		return false, f.createErr
	}
	f.updatedDomain, f.updateDomainArg = id, u
	return !f.updateDomainMissing, nil
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
func (f *fakeDir) ListOrgs() ([]directory.OrgInfo, error) {
	out := make([]directory.OrgInfo, 0, len(f.orgs))
	for _, o := range f.orgs {
		out = append(out, o)
	}
	return out, nil
}
func (f *fakeDir) GetOrg(id int64) (directory.OrgInfo, bool, error) {
	o, ok := f.orgs[id]
	return o, ok, nil
}
func (f *fakeDir) CreateOrg(name, description string) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	if f.orgs == nil {
		f.orgs = map[int64]directory.OrgInfo{}
	}
	f.nextOrgID++
	f.orgs[f.nextOrgID] = directory.OrgInfo{ID: f.nextOrgID, Name: name, Description: description}
	return f.nextOrgID, nil
}
func (f *fakeDir) UpdateOrg(id int64, name, description string) (bool, error) {
	o, ok := f.orgs[id]
	if !ok {
		return false, nil
	}
	o.Name, o.Description = name, description
	f.orgs[id] = o
	return true, nil
}
func (f *fakeDir) DeleteOrg(id int64) (bool, error) {
	if id == 0 {
		return false, errors.New("cannot delete the reserved organizationless id 0")
	}
	if _, ok := f.orgs[id]; !ok {
		return false, nil
	}
	delete(f.orgs, id)
	return true, nil
}
func (f *fakeDir) AssignDomainToOrg(domainID, orgID int64) (bool, error) {
	f.assignDomainID, f.assignOrgID = domainID, orgID
	return !f.assignDomainMissing, nil
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
func (f *fakeDir) CreateTask(taskType, params, createdBy string) (int64, error) {
	id := int64(len(f.tasks) + 1)
	f.tasks = append(f.tasks, directory.TaskInfo{
		ID: id, Type: taskType, Status: directory.TaskPending, Params: params, CreatedBy: createdBy,
	})
	return id, nil
}
func (f *fakeDir) ListTasks(int) ([]directory.TaskInfo, error) { return f.tasks, nil }
func (f *fakeDir) GetTask(id int64) (directory.TaskInfo, bool, error) {
	for _, t := range f.tasks {
		if t.ID == id {
			return t, true, nil
		}
	}
	return directory.TaskInfo{}, false, nil
}
func (f *fakeDir) ClaimNextTask() (directory.TaskInfo, bool, error) {
	for i := range f.tasks {
		if f.tasks[i].Status == directory.TaskPending {
			f.tasks[i].Status = directory.TaskRunning
			return f.tasks[i], true, nil
		}
	}
	return directory.TaskInfo{}, false, nil
}
func (f *fakeDir) FinishTask(id int64, status, message string) error {
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			f.tasks[i].Status, f.tasks[i].Message = status, message
			return nil
		}
	}
	return nil
}
func (f *fakeDir) RecentSpamVerdicts(int) ([]directory.SpamVerdict, error) { return f.verdicts, nil }
func (f *fakeDir) GetDefaultSyncPolicy() (easpolicy.Policy, error) {
	return f.defaultSyncPolicy, nil
}
func (f *fakeDir) SetDefaultSyncPolicy(p easpolicy.Policy) error {
	f.defaultSyncPolicy = p
	return nil
}
func (f *fakeDir) GetDomainSyncPolicy(string) (easpolicy.Policy, error) {
	return f.domainSyncPolicy, nil
}
func (f *fakeDir) GetCreateDefaults(scopeID int64) (directory.CreateDefaults, bool, error) {
	cd, ok := f.createDefaults[scopeID]
	return cd, ok, nil
}
func (f *fakeDir) EffectiveUserDefaults(int64) (directory.ResolvedUserDefaults, error) {
	return f.effectiveUserDefaults, nil
}
func (f *fakeDir) ListActiveSessions(int64) ([]directory.SessionRecord, error) {
	return f.activeSessions, nil
}
func (f *fakeDir) SetCreateDefaults(scopeID int64, cd directory.CreateDefaults) error {
	if f.createDefaults == nil {
		f.createDefaults = map[int64]directory.CreateDefaults{}
	}
	f.createDefaults[scopeID] = cd
	f.setCreateDefaultsScope = scopeID
	return nil
}
func (f *fakeDir) DeleteCreateDefaults(scopeID int64) (bool, error) {
	_, ok := f.createDefaults[scopeID]
	delete(f.createDefaults, scopeID)
	f.deletedCreateDefaultsScope = scopeID
	return ok, nil
}
func (f *fakeDir) SetDomainSyncPolicy(domain string, p easpolicy.Policy) (bool, error) {
	f.setDomainSyncPolicyDomain, f.domainSyncPolicy = domain, p
	return !f.domainSyncPolicyMissing, nil
}
func (f *fakeDir) ListFetchmail(mailbox string) ([]directory.FetchmailEntry, error) {
	return f.fetchmail[mailbox], nil
}
func (f *fakeDir) CreateFetchmail(e directory.FetchmailEntry) (int64, error) {
	if err := e.Validate(); err != nil {
		return 0, err
	}
	if f.fetchmail == nil {
		f.fetchmail = map[string][]directory.FetchmailEntry{}
	}
	f.nextFMID++
	e.ID = f.nextFMID
	f.fetchmail[e.Mailbox] = append(f.fetchmail[e.Mailbox], e)
	return e.ID, nil
}
func (f *fakeDir) DeleteFetchmail(id int64) (bool, error) {
	for mb, list := range f.fetchmail {
		for i, e := range list {
			if e.ID == id {
				f.fetchmail[mb] = append(list[:i], list[i+1:]...)
				return true, nil
			}
		}
	}
	return false, nil
}
func (f *fakeDir) GetUser(username string) (directory.UserDetail, bool, error) {
	f.gotUser = username
	if f.knownUsers != nil {
		u, ok := f.knownUsers[strings.ToLower(strings.TrimSpace(username))]
		return u, ok, nil
	}
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
func (f *fakeDir) ListUsersInDomain(domainID int64) ([]directory.UserInfo, error) {
	var out []directory.UserInfo
	for _, u := range f.users {
		if u.DomainID == domainID {
			out = append(out, u)
		}
	}
	return out, nil
}
func (f *fakeDir) ListContactsInDomain(int64) ([]directory.ContactInfo, error) {
	return f.contacts, nil
}
func (f *fakeDir) ListMListsInDomain(int64) ([]directory.MListInfo, error) { return f.mlists, nil }
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
func (f *fakeDir) Maildirs() ([]string, error) { return f.maildirs, nil }

// fakePaths derives resource paths under a fixed root for the tests.
type fakePaths struct{ root string }

func (p fakePaths) HomedirFor(domain string) string  { return p.root + "/dom/" + domain }
func (p fakePaths) MaildirFor(address string) string { return p.root + "/mbox/" + address }
func (p fakePaths) RelaySpoolPath() string           { return p.root + "/relay.sqlite3" }
func (p fakePaths) AntispamModelPath() string        { return p.root + "/antispam-model.json" }
func (p fakePaths) AntispamRulesPath() string        { return p.root + "/antispam-rules.cf" }

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

	sendAs       map[string][]string
	setSendAsDir string
	setSendAsVal []string

	meetingConfig    map[string]objectstore.MeetingConfig
	setMeetingDir    string
	setMeetingConfig objectstore.MeetingConfig

	storeOwners       map[string][]string
	setStoreOwnersDir string
	setStoreOwnersVal []string

	syncPolicy    map[string]easpolicy.Policy
	setSyncDir    string
	setSyncPolicy easpolicy.Policy

	folders     map[string][]objectstore.FolderInfo
	folderPerms map[string][]objectstore.PermissionEntry

	setPermDir    string
	setPermFolder int64
	setPermUser   string
	setPermRights uint32
	rmPermDir     string
	rmPermFolder  int64
	rmPermMember  int64
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

func (f *fakeStore) GetSendAs(maildir string) ([]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.sendAs[maildir], nil
}

func (f *fakeStore) GetStoreOwners(maildir string) ([]string, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.storeOwners[maildir], nil
}

func (f *fakeStore) GetSyncPolicy(maildir string) (easpolicy.Policy, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.syncPolicy[maildir], nil
}

func (f *fakeStore) SetSyncPolicy(maildir string, p easpolicy.Policy) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.syncPolicy == nil {
		f.syncPolicy = map[string]easpolicy.Policy{}
	}
	f.syncPolicy[maildir] = p
	f.setSyncDir, f.setSyncPolicy = maildir, p
	return nil
}

func (f *fakeStore) SetStoreOwners(maildir string, list []string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.storeOwners == nil {
		f.storeOwners = map[string][]string{}
	}
	f.storeOwners[maildir] = list
	f.setStoreOwnersDir, f.setStoreOwnersVal = maildir, list
	return nil
}

func (f *fakeStore) GetMeetingConfig(maildir string) (objectstore.MeetingConfig, error) {
	if f.getErr != nil {
		return objectstore.MeetingConfig{}, f.getErr
	}
	return f.meetingConfig[maildir], nil
}

func (f *fakeStore) SetMeetingConfig(maildir string, cfg objectstore.MeetingConfig) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.meetingConfig == nil {
		f.meetingConfig = map[string]objectstore.MeetingConfig{}
	}
	f.meetingConfig[maildir] = cfg
	f.setMeetingDir, f.setMeetingConfig = maildir, cfg
	return nil
}

func (f *fakeStore) SetSendAs(maildir string, list []string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.sendAs == nil {
		f.sendAs = map[string][]string{}
	}
	f.sendAs[maildir] = list
	f.setSendAsDir, f.setSendAsVal = maildir, list
	return nil
}

func (f *fakeStore) ListFolders(maildir string) ([]objectstore.FolderInfo, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.folders[maildir], nil
}

func (f *fakeStore) ListFolderPermissions(maildir string, folderID int64) ([]objectstore.PermissionEntry, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.folderPerms[maildir], nil
}

func (f *fakeStore) SetFolderPermission(maildir string, folderID int64, username string, rights uint32) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.setPermDir, f.setPermFolder, f.setPermUser, f.setPermRights = maildir, folderID, username, rights
	return nil
}

func (f *fakeStore) RemoveFolderPermission(maildir string, folderID, memberID int64) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.rmPermDir, f.rmPermFolder, f.rmPermMember = maildir, folderID, memberID
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

// adminServerDNS builds a server with a scripted DNS resolver for the health-check
// tests.
func adminServerDNS(t *testing.T, d Directory, resolver dnsResolver) *httptest.Server {
	t.Helper()
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = &fakeStore{}
	srv.resolver = resolver
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
