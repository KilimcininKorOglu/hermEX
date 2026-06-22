package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/ldapauth"
	"hermex/internal/logging"
	"hermex/internal/publicfolder"
)

// Directory is what the admin server needs from the account directory: password
// authentication, login-to-id resolution, and a user's admin roles.
type Directory interface {
	Authenticate(user, password string) (mailboxPath string, ok bool)
	UserID(login string) (id int64, ok bool, err error)
	AdminRoles(userID int64) ([]directory.AdminRole, error)
	EffectivePermissions(userID int64) ([]directory.Permission, error)
	GrantAdminRole(userID int64, role string, scopeID int64) error
	RevokeAdminRole(userID int64, role string, scopeID int64) error
	ListRoles() ([]directory.RoleInfo, error)
	GetRole(id int64) (directory.RoleDetail, bool, error)
	CreateRole(name, description string, perms []directory.Permission, userIDs []int64) (int64, error)
	UpdateRole(id int64, name, description string, perms []directory.Permission, userIDs []int64) (bool, error)
	DeleteRole(id int64) (bool, error)
	ListDomains() ([]directory.DomainInfo, error)
	ListUsers() ([]directory.UserInfo, error)
	ListUsersInDomain(domainID int64) ([]directory.UserInfo, error)
	Maildirs() ([]string, error)
	ListAliases() ([]directory.AliasInfo, error)
	CreateDomain(domainname, homedir string) (int64, error)
	GetDomain(id int64) (directory.DomainDetail, bool, error)
	UpdateDomain(id int64, u directory.DomainUpdate) (bool, error)
	PurgeDomain(domainID int64, deleteFiles bool) (bool, error)
	CreateUser(username, password, maildir string) (int64, error)
	SetPassword(username, password string) (bool, error)
	GetUser(username string) (directory.UserDetail, bool, error)
	UpdateUser(username string, u directory.UserUpdate) (bool, error)
	DeleteUser(username string, deleteFiles bool) (bool, error)
	ListAltnames(username string) ([]string, error)
	SetAltnames(username string, altnames []string) (bool, error)
	ListAliasesFor(username string) ([]string, error)
	SetAliasesFor(username string, aliases []string) (bool, error)
	GetForward(address string) (directory.ForwardInfo, bool, error)
	SetForward(username string, forwardType int, destination string) (bool, error)
	GetUserProperties(username string) (map[uint32]string, error)
	SetUserProperties(username string, props map[uint32]string) (bool, error)
	CreateAlias(aliasname, mainname string) error
	ListMLists() ([]directory.MListInfo, error)
	ListMListsInDomain(domainID int64) ([]directory.MListInfo, error)
	CreateMList(listname string, listType, listPriv int) (int64, error)
	DeleteMList(listname string) (bool, error)
	ListMembers(listname string) ([]string, error)
	SetMembers(listname string, members []string) (bool, error)
	ListSpecifieds(listname string) ([]string, error)
	SetSpecifieds(listname string, senders []string) (bool, error)
	ListContacts() ([]directory.ContactInfo, error)
	ListContactsInDomain(domainID int64) ([]directory.ContactInfo, error)
	CreateContact(email, displayName, domain string) (int64, error)
	UpdateContact(email, displayName string) (bool, error)
	DeleteContact(email string) (bool, error)
	ListOrgs() ([]directory.OrgInfo, error)
	GetOrg(id int64) (directory.OrgInfo, bool, error)
	CreateOrg(name, description string) (int64, error)
	UpdateOrg(id int64, name, description string) (bool, error)
	DeleteOrg(id int64) (bool, error)
	AssignDomainToOrg(domainID, orgID int64) (bool, error)
	GetLDAPConfig(orgID int64) (directory.LDAPConfig, bool, error)
	SetLDAPConfig(orgID int64, cfg directory.LDAPConfig) error
	UpsertLDAPUser(username string, externid []byte, maildir string) (created bool, err error)
	GetDefaultSyncPolicy() (easpolicy.Policy, error)
	SetDefaultSyncPolicy(p easpolicy.Policy) error
	GetDomainSyncPolicy(domain string) (easpolicy.Policy, error)
	SetDomainSyncPolicy(domain string, p easpolicy.Policy) (bool, error)
	GetCreateDefaults(scopeID int64) (directory.CreateDefaults, bool, error)
	SetCreateDefaults(scopeID int64, cd directory.CreateDefaults) error
	DeleteCreateDefaults(scopeID int64) (bool, error)
	EffectiveUserDefaults(domainID int64) (directory.ResolvedUserDefaults, error)
	ListActiveSessions(now int64) ([]directory.SessionRecord, error)
	ListFetchmail(mailbox string) ([]directory.FetchmailEntry, error)
	CreateFetchmail(e directory.FetchmailEntry) (int64, error)
	DeleteFetchmail(id int64) (bool, error)
	CreateTask(taskType, params, createdBy string) (int64, error)
	ListTasks(limit int) ([]directory.TaskInfo, error)
	GetTask(id int64) (directory.TaskInfo, bool, error)
	ClaimNextTask() (directory.TaskInfo, bool, error)
	FinishTask(id int64, status, message string) error
	RecentSpamVerdicts(limit int) ([]directory.SpamVerdict, error)
	GetAntispamSettings() (directory.AntispamSettings, bool, error)
	SetAntispamSettings(directory.AntispamSettings) error
	ListSenderRules() ([]directory.SenderRule, error)
	SetSenderRule(pattern, action string) error
	DeleteSenderRule(pattern string) (bool, error)
	GetGreylistEnabled() (bool, error)
	SetGreylistEnabled(on bool) error
	GetRateLimitSettings() (directory.RateLimitSettings, bool, error)
	SetRateLimitSettings(directory.RateLimitSettings) error
	GetOutboundSettings() (directory.OutboundSettings, bool, error)
	SetOutboundSettings(directory.OutboundSettings) error
	GetDigestSettings() (directory.DigestSettings, bool, error)
	SetDigestSettings(directory.DigestSettings) error
	SetDKIMKey(domain, selector string, privPEM []byte, publicTXT string) error
	SetDKIMEnabled(domain string, enabled bool) error
	GetDKIMKeyInfo(domain string) (directory.DKIMKeyInfo, bool, error)
	DeleteDKIMKey(domain string) error
	GetUserSpamThreshold(username string) (*int, error)
	SetUserSpamThreshold(username string, threshold *int) error
	GetDomainSpamThreshold(domain string) (*int, error)
	SetDomainSpamThreshold(domain string, threshold *int) error
}

// LDAPSyncer downsyncs an organization's directory accounts. It is optional —
// the Directory Sync page reports sync as unavailable when none is set. The
// concrete *ldapauth.Verifier satisfies it.
type LDAPSyncer interface {
	Sync(cfg directory.LDAPConfig) ([]ldapauth.SyncedUser, error)
}

// Paths derives a new domain's homedir and a new user's maildir from the
// configured data root; *config.Config satisfies it.
type Paths interface {
	HomedirFor(domain string) string
	MaildirFor(address string) string
	RelaySpoolPath() string
	AntispamModelPath() string
	AntispamRulesPath() string
}

// LogReader queries the central log store for the log viewer. It is optional —
// the viewer reports logging as unconfigured when none is set.
type LogReader interface {
	Recent(ctx context.Context, subsystem string, limit int64) ([]logging.LogEntry, error)
}

const (
	sessionCookie = "hermex_admin"
	csrfCookie    = "hermex_admin_csrf"
	csrfHeader    = "X-CSRF-Token"
	sessionTTL    = 8 * time.Hour
)

// ctxKey is the context key the auth middleware stores the session claims under.
type ctxKey struct{}

// Server answers the admin API. Build one with NewServer.
type Server struct {
	dir           Directory
	paths         Paths
	secret        []byte
	logs          LogReader
	syncer        LDAPSyncer
	store         MailboxStore
	pub           *publicfolder.Service
	mailq         MailQueue
	resolver      dnsResolver
	healthTargets []HealthTarget
}

// NewServer builds an admin server backed by the directory, deriving new
// resources' on-disk paths with paths and signing sessions with secret. The
// mailbox store opens each user's object store on demand for the store-backed
// tabs (out-of-office).
func NewServer(dir Directory, paths Paths, secret []byte) *Server {
	return &Server{
		dir: dir, paths: paths, secret: secret,
		store: mailboxStore{}, pub: publicfolder.New(paths),
		mailq: relaySpool{path: paths.RelaySpoolPath()}, resolver: net.DefaultResolver,
	}
}

// SetLogReader attaches a log store reader, enabling the log viewer.
func (s *Server) SetLogReader(r LogReader) { s.logs = r }

// SetLDAPSyncer attaches a directory syncer, enabling the Directory Sync trigger.
func (s *Server) SetLDAPSyncer(syncer LDAPSyncer) { s.syncer = syncer }

// Handler returns the admin HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.Handle("POST /admin/logout", s.protect(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /admin/whoami", s.protect(http.HandlerFunc(s.handleWhoami)))
	// Domain list/create: list is scope-filtered in the handler; creating a new
	// domain is a system-level operation, not a domain admin's.
	mux.Handle("GET /admin/domains", s.protect(http.HandlerFunc(s.handleListDomains)))
	mux.Handle("POST /admin/domains", s.protect(s.requireSystem(s.handleCreateDomain)))
	mux.Handle("GET /admin/domains/{domainID}", s.protect(http.HandlerFunc(s.handleGetDomain)))
	mux.Handle("PUT /admin/domains/{domainID}", s.protect(s.requireSystem(s.handleUpdateDomain)))
	mux.Handle("DELETE /admin/domains/{domainID}", s.protect(s.requirePurge(s.handleDeleteDomain)))
	// User list/create: list is scope-filtered, create checks the new user's
	// domain scope — both in the handler since the target is not a path value.
	mux.Handle("GET /admin/users", s.protect(http.HandlerFunc(s.handleListUsers)))
	mux.Handle("POST /admin/users", s.protect(http.HandlerFunc(s.handleCreateUser)))
	// Per-user management routes are domain-scoped (requireUserScope): a domain
	// admin may manage users in its domain. The role-assignment routes below are
	// the exception — they stay full-system-admin-only.
	mux.Handle("GET /admin/users/{email}", s.protect(s.requireUserScope(s.handleGetUser)))
	mux.Handle("PUT /admin/users/{email}", s.protect(s.requireUserScope(s.handleUpdateUser)))
	mux.Handle("DELETE /admin/users/{email}", s.protect(s.requireUserScope(s.handleDeleteUser)))
	mux.Handle("GET /admin/users/{email}/altnames", s.protect(s.requireUserScope(s.handleListAltnames)))
	mux.Handle("PUT /admin/users/{email}/altnames", s.protect(s.requireUserScope(s.handleSetAltnames)))
	mux.Handle("GET /admin/users/{email}/aliases", s.protect(s.requireUserScope(s.handleListUserAliases)))
	mux.Handle("PUT /admin/users/{email}/aliases", s.protect(s.requireUserScope(s.handleSetUserAliases)))
	mux.Handle("GET /admin/users/{email}/forward", s.protect(s.requireUserScope(s.handleGetUserForward)))
	mux.Handle("PUT /admin/users/{email}/forward", s.protect(s.requireUserScope(s.handleSetUserForward)))
	mux.Handle("GET /admin/users/{email}/folders", s.protect(s.requireUserScope(s.handleListUserFolders)))
	mux.Handle("GET /admin/users/{email}/folders/{fid}/permissions", s.protect(s.requireUserScope(s.handleListFolderPermissions)))
	mux.Handle("PUT /admin/users/{email}/folders/{fid}/permissions", s.protect(s.requireUserScope(s.handleSetFolderPermission)))
	mux.Handle("DELETE /admin/users/{email}/folders/{fid}/permissions", s.protect(s.requireUserScope(s.handleRemoveFolderPermission)))
	mux.Handle("GET /admin/users/{email}/sendas", s.protect(s.requireUserScope(s.handleGetUserSendAs)))
	mux.Handle("PUT /admin/users/{email}/sendas", s.protect(s.requireUserScope(s.handleSetUserSendAs)))
	mux.Handle("GET /admin/users/{email}/meeting", s.protect(s.requireUserScope(s.handleGetUserMeeting)))
	mux.Handle("PUT /admin/users/{email}/meeting", s.protect(s.requireUserScope(s.handleSetUserMeeting)))
	mux.Handle("GET /admin/users/{email}/storeowners", s.protect(s.requireUserScope(s.handleGetUserStoreOwners)))
	mux.Handle("PUT /admin/users/{email}/storeowners", s.protect(s.requireUserScope(s.handleSetUserStoreOwners)))
	mux.Handle("GET /admin/users/{email}/syncpolicy", s.protect(s.requireUserScope(s.handleGetUserSyncPolicy)))
	mux.Handle("PUT /admin/users/{email}/syncpolicy", s.protect(s.requireUserScope(s.handleSetUserSyncPolicy)))
	mux.Handle("GET /admin/syncpolicy", s.protect(s.requireSystem(s.handleGetDefaultSyncPolicy)))
	mux.Handle("PUT /admin/syncpolicy", s.protect(s.requireSystem(s.handleSetDefaultSyncPolicy)))
	mux.Handle("GET /admin/domains/{domainID}/syncpolicy", s.protect(s.requireSystem(s.handleGetDomainSyncPolicy)))
	mux.Handle("PUT /admin/domains/{domainID}/syncpolicy", s.protect(s.requireSystem(s.handleSetDomainSyncPolicy)))
	mux.Handle("GET /admin/domains/{domainID}/dnscheck", s.protect(s.requireSystem(s.handleGetDomainDNS)))
	mux.Handle("GET /admin/domains/{domainID}/createdefaults", s.protect(s.requireSystem(s.handleGetDomainDefaults)))
	mux.Handle("PUT /admin/domains/{domainID}/createdefaults", s.protect(s.requireSystem(s.handleSetDomainDefaults)))
	mux.Handle("GET /admin/defaults", s.protect(s.requireSystem(s.handleGetDefaults)))
	mux.Handle("PUT /admin/defaults", s.protect(s.requireSystem(s.handleSetDefaults)))
	mux.Handle("GET /admin/mailq", s.protect(s.requireSystem(s.handleGetMailq)))
	mux.Handle("POST /admin/mailq/{id}/retry", s.protect(s.requireSystem(s.handleRetryMailq)))
	mux.Handle("DELETE /admin/mailq/{id}", s.protect(s.requireSystem(s.handleDeleteMailq)))
	mux.Handle("GET /admin/status", s.protect(s.requireSystem(s.handleGetStatus)))
	mux.Handle("GET /admin/tasq/status", s.protect(s.requireSystem(s.handleGetTaskqStatus)))
	mux.Handle("GET /admin/mobile-devices", s.protect(s.requireSystem(s.handleGetMobileDevices)))
	mux.Handle("GET /admin/public-folders", s.protect(s.requireSystem(s.handleGetPublicFolders)))
	mux.Handle("GET /admin/users/{email}/fetchmail", s.protect(s.requireUserScope(s.handleListUserFetchmail)))
	mux.Handle("POST /admin/users/{email}/fetchmail", s.protect(s.requireUserScope(s.handleCreateUserFetchmail)))
	mux.Handle("DELETE /admin/users/{email}/fetchmail/{id}", s.protect(s.requireUserScope(s.handleDeleteUserFetchmail)))
	mux.Handle("GET /admin/users/{email}/contact", s.protect(s.requireUserScope(s.handleGetContact)))
	mux.Handle("PUT /admin/users/{email}/contact", s.protect(s.requireUserScope(s.handleSetContact)))
	mux.Handle("GET /admin/users/{email}/oof", s.protect(s.requireUserScope(s.handleGetUserOOF)))
	mux.Handle("PUT /admin/users/{email}/oof", s.protect(s.requireUserScope(s.handleSetUserOOF)))
	mux.Handle("GET /admin/users/{email}/devices", s.protect(s.requireUserScope(s.handleGetUserDevices)))
	mux.Handle("POST /admin/users/{email}/devices/action", s.protect(s.requireUserScope(s.handleUserDeviceAction)))
	mux.Handle("GET /admin/users/{email}/quota", s.protect(s.requireUserScope(s.handleGetUserQuota)))
	mux.Handle("PUT /admin/users/{email}/quota", s.protect(s.requireUserScope(s.handleSetUserQuota)))
	mux.Handle("POST /admin/users/{email}/password", s.protect(s.requirePasswordScope(s.handleSetPassword)))
	mux.Handle("GET /admin/users/{email}/roles", s.protect(s.requireSystem(s.handleListRoles)))
	mux.Handle("POST /admin/users/{email}/roles", s.protect(s.requireSystem(s.handleGrantRole)))
	mux.Handle("DELETE /admin/users/{email}/roles", s.protect(s.requireSystem(s.handleRevokeRole)))
	mux.Handle("GET /admin/aliases", s.protect(s.requireSystem(s.handleListAliases)))
	mux.Handle("POST /admin/aliases", s.protect(s.requireSystem(s.handleCreateAlias)))
	mux.Handle("GET /admin/orgs", s.protect(s.requireSystem(s.handleListOrgs)))
	mux.Handle("POST /admin/orgs", s.protect(s.requireSystem(s.handleCreateOrg)))
	mux.Handle("GET /admin/orgs/{orgID}", s.protect(s.requireSystem(s.handleGetOrg)))
	mux.Handle("PUT /admin/orgs/{orgID}", s.protect(s.requireSystem(s.handleUpdateOrg)))
	mux.Handle("DELETE /admin/orgs/{orgID}", s.protect(s.requireSystem(s.handleDeleteOrg)))
	mux.Handle("PUT /admin/orgs/{orgID}/domains/{domainID}", s.protect(s.requireSystem(s.handleAssignOrgDomain)))
	mux.Handle("DELETE /admin/orgs/{orgID}/domains/{domainID}", s.protect(s.requireSystem(s.handleUnassignOrgDomain)))
	mux.Handle("GET /admin/orgs/{orgID}/ldap", s.protect(http.HandlerFunc(s.handleGetLDAP)))
	mux.Handle("PUT /admin/orgs/{orgID}/ldap", s.protect(http.HandlerFunc(s.handlePutLDAP)))
	mux.Handle("GET /admin/roles", s.protect(s.requireSystem(s.handleRolesList)))
	mux.Handle("POST /admin/roles", s.protect(s.requireSystem(s.handleRoleCreate)))
	mux.Handle("GET /admin/roles/permissions", s.protect(s.requireSystem(s.handleRolePermissions)))
	mux.Handle("GET /admin/roles/{roleID}", s.protect(s.requireSystem(s.handleRoleGet)))
	mux.Handle("PUT /admin/roles/{roleID}", s.protect(s.requireSystem(s.handleRoleUpdate)))
	mux.Handle("DELETE /admin/roles/{roleID}", s.protect(s.requireSystem(s.handleRoleDelete)))

	// Web UI (server-rendered HTML) and its assets.
	mux.Handle("GET /admin/static/", staticHandler())
	mux.HandleFunc("GET /admin/ui/login", s.handleUILoginPage)
	mux.HandleFunc("POST /admin/ui/login", s.handleUILoginSubmit)
	mux.HandleFunc("POST /admin/ui/logout", s.handleUILogout)
	mux.HandleFunc("GET /admin/ui/users", s.handleUIUsers)
	mux.HandleFunc("POST /admin/ui/users", s.handleUICreateUser)
	mux.HandleFunc("GET /admin/ui/user-create-fields", s.handleUICreateUserDefaults)
	mux.HandleFunc("GET /admin/ui/users/{email}", s.handleUIUserDetail)
	mux.HandleFunc("PUT /admin/ui/users/{email}", s.handleUIUserEdit)
	mux.HandleFunc("POST /admin/ui/users/{email}/delete", s.handleUIUserDelete)
	mux.HandleFunc("PUT /admin/ui/users/{email}/altnames", s.handleUIUserAltnames)
	mux.HandleFunc("PUT /admin/ui/users/{email}/aliases", s.handleUIUserAliases)
	mux.HandleFunc("PUT /admin/ui/users/{email}/forward", s.handleUIUserForward)
	mux.HandleFunc("GET /admin/ui/users/{email}/folder-perms", s.handleUIFolderPerms)
	mux.HandleFunc("POST /admin/ui/users/{email}/folder-perms/set", s.handleUISetFolderPerm)
	mux.HandleFunc("POST /admin/ui/users/{email}/folder-perms/remove", s.handleUIRemoveFolderPerm)
	mux.HandleFunc("GET /admin/ui/users/{email}/quarantine", s.handleUIQuarantine)
	mux.HandleFunc("POST /admin/ui/users/{email}/quarantine/release", s.handleUIQuarantineRelease)
	mux.HandleFunc("POST /admin/ui/users/{email}/quarantine/delete", s.handleUIQuarantineDelete)
	mux.HandleFunc("PUT /admin/ui/users/{email}/delegates", s.handleUIUserDelegates)
	mux.HandleFunc("PUT /admin/ui/users/{email}/sendas", s.handleUIUserSendAs)
	mux.HandleFunc("PUT /admin/ui/users/{email}/meeting", s.handleUIUserMeeting)
	mux.HandleFunc("PUT /admin/ui/users/{email}/storeowners", s.handleUIUserStoreOwners)
	mux.HandleFunc("PUT /admin/ui/users/{email}/syncpolicy", s.handleUIUserSyncPolicy)
	mux.HandleFunc("POST /admin/ui/users/{email}/fetchmail", s.handleUIUserAddFetchmail)
	mux.HandleFunc("POST /admin/ui/users/{email}/fetchmail/{id}/delete", s.handleUIUserDeleteFetchmail)
	mux.HandleFunc("PUT /admin/ui/users/{email}/contact", s.handleUIUserContact)
	mux.HandleFunc("PUT /admin/ui/users/{email}/oof", s.handleUIUserOOF)
	mux.HandleFunc("POST /admin/ui/users/{email}/devices/action", s.handleUIUserDevices)
	mux.HandleFunc("PUT /admin/ui/users/{email}/quota", s.handleUIUserQuota)
	mux.HandleFunc("PUT /admin/ui/users/{email}/spam-threshold", s.handleUIUserSpamThreshold)
	mux.HandleFunc("PUT /admin/ui/users/{email}/hide", s.handleUIUserHide)
	mux.HandleFunc("POST /admin/ui/users/{email}/roles/grant", s.handleUIUserGrantRole)
	mux.HandleFunc("POST /admin/ui/users/{email}/roles/revoke", s.handleUIUserRevokeRole)
	mux.HandleFunc("GET /admin/ui/syncpolicy", s.handleUISyncPolicy)
	mux.HandleFunc("PUT /admin/ui/syncpolicy", s.handleUISaveSyncPolicy)
	mux.HandleFunc("GET /admin/ui/defaults", s.handleUIDefaults)
	mux.HandleFunc("PUT /admin/ui/defaults", s.handleUISaveDefaults)
	mux.HandleFunc("GET /admin/ui/mobile-devices", s.handleUIMobileDevices)
	mux.HandleFunc("GET /admin/ui/mobile-devices/panel", s.handleUIMobileDevicesPanel)
	mux.HandleFunc("GET /admin/ui/mailq", s.handleUIMailq)
	mux.HandleFunc("GET /admin/ui/mailq/panel", s.handleUIMailqPanel)
	mux.HandleFunc("POST /admin/ui/mailq/retry", s.handleUIMailqRetry)
	mux.HandleFunc("POST /admin/ui/mailq/delete", s.handleUIMailqDelete)
	mux.HandleFunc("GET /admin/ui/status", s.handleUIStatus)
	mux.HandleFunc("GET /admin/ui/status/panel", s.handleUIStatusPanel)
	mux.HandleFunc("GET /admin/ui/taskq", s.handleUITaskq)
	mux.HandleFunc("GET /admin/ui/taskq/panel", s.handleUITaskqPanel)
	mux.HandleFunc("GET /admin/ui/antispam", s.handleUIAntispam)
	mux.HandleFunc("POST /admin/ui/antispam/retrain", s.handleUIRetrainBayes)
	mux.HandleFunc("POST /admin/ui/antispam/settings", s.handleUISaveAntispamSettings)
	mux.HandleFunc("POST /admin/ui/antispam/greylist", s.handleUIToggleGreylist)
	mux.HandleFunc("POST /admin/ui/antispam/ratelimit", s.handleUISaveRateLimit)
	mux.HandleFunc("POST /admin/ui/antispam/outbound", s.handleUISaveOutbound)
	mux.HandleFunc("POST /admin/ui/antispam/digest", s.handleUISaveDigest)
	mux.HandleFunc("GET /admin/ui/spam-history", s.handleUISpamHistory)
	mux.HandleFunc("GET /admin/ui/sender-access", s.handleUISenderAccess)
	mux.HandleFunc("POST /admin/ui/sender-access", s.handleUISaveSenderRule)
	mux.HandleFunc("POST /admin/ui/sender-access/delete", s.handleUIDeleteSenderRule)
	mux.HandleFunc("GET /admin/ui/public-folders", s.handleUIPublicFolders)
	mux.HandleFunc("GET /admin/ui/public-folders/panel", s.handleUIPublicFoldersPanel)
	mux.HandleFunc("POST /admin/ui/public-folders/folder", s.handleUICreatePublicFolder)
	mux.HandleFunc("POST /admin/ui/public-folders/folder/delete", s.handleUIDeletePublicFolder)
	mux.HandleFunc("POST /admin/ui/public-folders/grant", s.handleUISetPublicGrant)
	mux.HandleFunc("POST /admin/ui/public-folders/grant/remove", s.handleUIRemovePublicGrant)
	mux.HandleFunc("GET /admin/ui/orgs", s.handleUIOrgs)
	mux.HandleFunc("POST /admin/ui/orgs", s.handleUICreateOrg)
	mux.HandleFunc("GET /admin/ui/orgs/{orgID}", s.handleUIOrgDetail)
	mux.HandleFunc("PUT /admin/ui/orgs/{orgID}", s.handleUIUpdateOrg)
	mux.HandleFunc("POST /admin/ui/orgs/{orgID}/delete", s.handleUIDeleteOrg)
	mux.HandleFunc("POST /admin/ui/orgs/{orgID}/domains", s.handleUIOrgAttachDomain)
	mux.HandleFunc("POST /admin/ui/orgs/{orgID}/domains/{domainID}/delete", s.handleUIOrgDetachDomain)
	mux.HandleFunc("GET /admin/ui/roles", s.handleUIRoles)
	mux.HandleFunc("POST /admin/ui/roles", s.handleUICreateRole)
	mux.HandleFunc("GET /admin/ui/roles/{roleID}", s.handleUIRoleDetail)
	mux.HandleFunc("PUT /admin/ui/roles/{roleID}", s.handleUIUpdateRole)
	mux.HandleFunc("POST /admin/ui/roles/{roleID}/delete", s.handleUIDeleteRole)
	mux.HandleFunc("GET /admin/ui/domains", s.handleUIDomains)
	mux.HandleFunc("POST /admin/ui/domains", s.handleUICreateDomain)
	mux.HandleFunc("GET /admin/ui/domains/{domainID}", s.handleUIDomainDetail)
	mux.HandleFunc("PUT /admin/ui/domains/{domainID}", s.handleUISaveDomain)
	mux.HandleFunc("PUT /admin/ui/domains/{domainID}/syncpolicy", s.handleUISaveDomainSyncPolicy)
	mux.HandleFunc("PUT /admin/ui/domains/{domainID}/spam-threshold", s.handleUIDomainSpamThreshold)
	mux.HandleFunc("PUT /admin/ui/domains/{domainID}/createdefaults", s.handleUISaveDomainDefaults)
	mux.HandleFunc("POST /admin/ui/domains/{domainID}/dkim/generate", s.handleUIDKIMGenerate)
	mux.HandleFunc("PUT /admin/ui/domains/{domainID}/dkim/enable", s.handleUIDKIMEnable)
	mux.HandleFunc("POST /admin/ui/domains/{domainID}/dkim/delete", s.handleUIDKIMDelete)
	mux.HandleFunc("GET /admin/ui/domains/{domainID}/dnscheck", s.handleUIDomainDNS)
	mux.HandleFunc("POST /admin/ui/domains/{domainID}/purge", s.handleUIPurgeDomain)
	mux.HandleFunc("GET /admin/ui/aliases", s.handleUIAliases)
	mux.HandleFunc("POST /admin/ui/aliases", s.handleUICreateAlias)
	mux.HandleFunc("GET /admin/ui/mlists", s.handleUIMLists)
	mux.HandleFunc("POST /admin/ui/mlists", s.handleUICreateMList)
	mux.HandleFunc("GET /admin/ui/mlists/{addr}", s.handleUIMListDetail)
	mux.HandleFunc("PUT /admin/ui/mlists/{addr}/members", s.handleUIMListMembers)
	mux.HandleFunc("PUT /admin/ui/mlists/{addr}/specifieds", s.handleUIMListSpecifieds)
	mux.HandleFunc("POST /admin/ui/mlists/{addr}/delete", s.handleUIDeleteMList)
	mux.HandleFunc("GET /admin/ui/contacts", s.handleUIContacts)
	mux.HandleFunc("POST /admin/ui/contacts", s.handleUICreateContact)
	mux.HandleFunc("PUT /admin/ui/contacts/{email}", s.handleUIUpdateContact)
	mux.HandleFunc("POST /admin/ui/contacts/{email}/delete", s.handleUIDeleteContact)
	mux.HandleFunc("GET /admin/ui/logs", s.handleUILogs)
	mux.HandleFunc("GET /admin/ui/ldap", s.handleUILDAP)
	mux.HandleFunc("POST /admin/ui/ldap", s.handleUISaveLDAP)
	mux.HandleFunc("POST /admin/ui/ldap/sync", s.handleUISyncLDAP)
	mux.HandleFunc("GET /admin/ui/", s.handleUIDashboard)
	return mux
}

// handleLogin authenticates an administrator and, on success, sets the session
// and CSRF cookies. Authentication requires valid credentials AND at least one
// admin role; both wrong credentials and a valid non-admin yield 401, never
// revealing that the credentials were correct. The login must be the account's
// primary address; an alias is not resolved here.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	uid, roles, ok, err := s.authAdmin(req.Login, req.Password)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	session, csrf := s.issueSession(req.Login, uid)
	setSessionCookies(w, session, csrf)
	writeJSON(w, map[string]any{"login": req.Login, "roles": roles, "csrfToken": csrf})
}

// authAdmin authenticates a login and returns the user id and admin roles when
// the credentials are valid AND the user holds administrative authority. Whether
// the user is an admin is decided through the single permission path (named roles
// or a bridged tier grant), so an admin granted authority only by a named role
// can sign in; the returned roles remain the legacy tier grants for the response.
// ok is false alike for wrong credentials, an unknown user, and a non-admin; err
// is set only for an infrastructure failure.
func (s *Server) authAdmin(login, password string) (uid int64, roles []directory.AdminRole, ok bool, err error) {
	if _, authed := s.dir.Authenticate(login, password); !authed {
		return 0, nil, false, nil
	}
	id, found, err := s.dir.UserID(login)
	if err != nil {
		return 0, nil, false, err
	}
	if !found {
		return 0, nil, false, nil
	}
	perms, err := s.dir.EffectivePermissions(id)
	if err != nil {
		return 0, nil, false, err
	}
	if len(perms) == 0 {
		return 0, nil, false, nil
	}
	r, err := s.dir.AdminRoles(id)
	if err != nil {
		return 0, nil, false, err
	}
	return id, r, true, nil
}

// issueSession mints the session and CSRF tokens for an authenticated admin.
func (s *Server) issueSession(login string, uid int64) (session, csrf string) {
	session = signToken(s.secret, claims{
		Login:  login,
		UserID: uid,
		Expiry: time.Now().Add(sessionTTL).Unix(),
	})
	return session, newCSRFToken()
}

// setSessionCookies writes the session and CSRF cookies. The session cookie is
// HttpOnly; the CSRF cookie is readable so the client echoes it back (the
// double-submit token).
func setSessionCookies(w http.ResponseWriter, session, csrf string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: session, Path: "/admin",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/admin",
		MaxAge: int(sessionTTL.Seconds()), Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/admin", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleWhoami reports the authenticated admin's identity and current roles.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	cl := r.Context().Value(ctxKey{}).(claims)
	roles, err := s.dir.AdminRoles(cl.UserID)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"login": cl.Login, "roles": roles})
}

// protect wraps a handler so it runs only with a valid session cookie, and — for
// a state-changing method — a matching CSRF token (double-submit: the
// X-CSRF-Token header must equal the CSRF cookie). It stashes the claims in the
// request context.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		cl, err := verifyToken(s.secret, c.Value)
		if err != nil {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		if isUnsafeMethod(r.Method) && !validCSRF(r) {
			http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, cl)))
	})
}

// newCSRFToken mints a random double-submit CSRF token.
func newCSRFToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// isUnsafeMethod reports whether an HTTP method changes state and so needs CSRF
// protection.
func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// validCSRF reports whether the request carries a CSRF header equal to its CSRF
// cookie (compared in constant time).
func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get(csrfHeader)
	return header != "" && hmac.Equal([]byte(cookie.Value), []byte(header))
}

// writeJSON encodes v as a 200 JSON response body.
func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

// writeJSONStatus writes status and encodes v as the JSON response body. The
// content type must be set before the status, so callers go through here rather
// than calling WriteHeader directly.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
