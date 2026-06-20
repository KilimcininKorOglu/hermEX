package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/ldapauth"
	"hermex/internal/logging"
)

// Directory is what the admin server needs from the account directory: password
// authentication, login-to-id resolution, and a user's admin roles.
type Directory interface {
	Authenticate(user, password string) (mailboxPath string, ok bool)
	UserID(login string) (id int64, ok bool, err error)
	AdminRoles(userID int64) ([]directory.AdminRole, error)
	GrantAdminRole(userID int64, role string, scopeID int64) error
	RevokeAdminRole(userID int64, role string, scopeID int64) error
	ListDomains() ([]directory.DomainInfo, error)
	ListUsers() ([]directory.UserInfo, error)
	ListAliases() ([]directory.AliasInfo, error)
	CreateDomain(domainname, homedir string) (int64, error)
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
	CreateMList(listname string, listType, listPriv int) (int64, error)
	DeleteMList(listname string) (bool, error)
	ListMembers(listname string) ([]string, error)
	SetMembers(listname string, members []string) (bool, error)
	ListSpecifieds(listname string) ([]string, error)
	SetSpecifieds(listname string, senders []string) (bool, error)
	ListContacts() ([]directory.ContactInfo, error)
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
	ListFetchmail(mailbox string) ([]directory.FetchmailEntry, error)
	CreateFetchmail(e directory.FetchmailEntry) (int64, error)
	DeleteFetchmail(id int64) (bool, error)
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
	dir    Directory
	paths  Paths
	secret []byte
	logs   LogReader
	syncer LDAPSyncer
	store  MailboxStore
}

// NewServer builds an admin server backed by the directory, deriving new
// resources' on-disk paths with paths and signing sessions with secret. The
// mailbox store opens each user's object store on demand for the store-backed
// tabs (out-of-office).
func NewServer(dir Directory, paths Paths, secret []byte) *Server {
	return &Server{dir: dir, paths: paths, secret: secret, store: mailboxStore{}}
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
	mux.Handle("GET /admin/domains", s.protect(s.requireSystem(s.handleListDomains)))
	mux.Handle("POST /admin/domains", s.protect(s.requireSystem(s.handleCreateDomain)))
	mux.Handle("GET /admin/users", s.protect(s.requireSystem(s.handleListUsers)))
	mux.Handle("POST /admin/users", s.protect(s.requireSystem(s.handleCreateUser)))
	mux.Handle("GET /admin/users/{email}", s.protect(s.requireSystem(s.handleGetUser)))
	mux.Handle("PUT /admin/users/{email}", s.protect(s.requireSystem(s.handleUpdateUser)))
	mux.Handle("DELETE /admin/users/{email}", s.protect(s.requireSystem(s.handleDeleteUser)))
	mux.Handle("GET /admin/users/{email}/altnames", s.protect(s.requireSystem(s.handleListAltnames)))
	mux.Handle("PUT /admin/users/{email}/altnames", s.protect(s.requireSystem(s.handleSetAltnames)))
	mux.Handle("GET /admin/users/{email}/aliases", s.protect(s.requireSystem(s.handleListUserAliases)))
	mux.Handle("PUT /admin/users/{email}/aliases", s.protect(s.requireSystem(s.handleSetUserAliases)))
	mux.Handle("GET /admin/users/{email}/forward", s.protect(s.requireSystem(s.handleGetUserForward)))
	mux.Handle("PUT /admin/users/{email}/forward", s.protect(s.requireSystem(s.handleSetUserForward)))
	mux.Handle("GET /admin/users/{email}/folders", s.protect(s.requireSystem(s.handleListUserFolders)))
	mux.Handle("GET /admin/users/{email}/folders/{fid}/permissions", s.protect(s.requireSystem(s.handleListFolderPermissions)))
	mux.Handle("PUT /admin/users/{email}/folders/{fid}/permissions", s.protect(s.requireSystem(s.handleSetFolderPermission)))
	mux.Handle("DELETE /admin/users/{email}/folders/{fid}/permissions", s.protect(s.requireSystem(s.handleRemoveFolderPermission)))
	mux.Handle("GET /admin/users/{email}/sendas", s.protect(s.requireSystem(s.handleGetUserSendAs)))
	mux.Handle("PUT /admin/users/{email}/sendas", s.protect(s.requireSystem(s.handleSetUserSendAs)))
	mux.Handle("GET /admin/users/{email}/meeting", s.protect(s.requireSystem(s.handleGetUserMeeting)))
	mux.Handle("PUT /admin/users/{email}/meeting", s.protect(s.requireSystem(s.handleSetUserMeeting)))
	mux.Handle("GET /admin/users/{email}/storeowners", s.protect(s.requireSystem(s.handleGetUserStoreOwners)))
	mux.Handle("PUT /admin/users/{email}/storeowners", s.protect(s.requireSystem(s.handleSetUserStoreOwners)))
	mux.Handle("GET /admin/users/{email}/syncpolicy", s.protect(s.requireSystem(s.handleGetUserSyncPolicy)))
	mux.Handle("PUT /admin/users/{email}/syncpolicy", s.protect(s.requireSystem(s.handleSetUserSyncPolicy)))
	mux.Handle("GET /admin/syncpolicy", s.protect(s.requireSystem(s.handleGetDefaultSyncPolicy)))
	mux.Handle("PUT /admin/syncpolicy", s.protect(s.requireSystem(s.handleSetDefaultSyncPolicy)))
	mux.Handle("GET /admin/users/{email}/fetchmail", s.protect(s.requireSystem(s.handleListUserFetchmail)))
	mux.Handle("POST /admin/users/{email}/fetchmail", s.protect(s.requireSystem(s.handleCreateUserFetchmail)))
	mux.Handle("DELETE /admin/users/{email}/fetchmail/{id}", s.protect(s.requireSystem(s.handleDeleteUserFetchmail)))
	mux.Handle("GET /admin/users/{email}/contact", s.protect(s.requireSystem(s.handleGetContact)))
	mux.Handle("PUT /admin/users/{email}/contact", s.protect(s.requireSystem(s.handleSetContact)))
	mux.Handle("GET /admin/users/{email}/oof", s.protect(s.requireSystem(s.handleGetUserOOF)))
	mux.Handle("PUT /admin/users/{email}/oof", s.protect(s.requireSystem(s.handleSetUserOOF)))
	mux.Handle("GET /admin/users/{email}/devices", s.protect(s.requireSystem(s.handleGetUserDevices)))
	mux.Handle("POST /admin/users/{email}/devices/action", s.protect(s.requireSystem(s.handleUserDeviceAction)))
	mux.Handle("GET /admin/users/{email}/quota", s.protect(s.requireSystem(s.handleGetUserQuota)))
	mux.Handle("PUT /admin/users/{email}/quota", s.protect(s.requireSystem(s.handleSetUserQuota)))
	mux.Handle("POST /admin/users/{email}/password", s.protect(s.requireSystem(s.handleSetPassword)))
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

	// Web UI (server-rendered HTML) and its assets.
	mux.Handle("GET /admin/static/", staticHandler())
	mux.HandleFunc("GET /admin/ui/login", s.handleUILoginPage)
	mux.HandleFunc("POST /admin/ui/login", s.handleUILoginSubmit)
	mux.HandleFunc("POST /admin/ui/logout", s.handleUILogout)
	mux.HandleFunc("GET /admin/ui/users", s.handleUIUsers)
	mux.HandleFunc("POST /admin/ui/users", s.handleUICreateUser)
	mux.HandleFunc("GET /admin/ui/users/{email}", s.handleUIUserDetail)
	mux.HandleFunc("PUT /admin/ui/users/{email}", s.handleUIUserEdit)
	mux.HandleFunc("POST /admin/ui/users/{email}/delete", s.handleUIUserDelete)
	mux.HandleFunc("PUT /admin/ui/users/{email}/altnames", s.handleUIUserAltnames)
	mux.HandleFunc("PUT /admin/ui/users/{email}/aliases", s.handleUIUserAliases)
	mux.HandleFunc("PUT /admin/ui/users/{email}/forward", s.handleUIUserForward)
	mux.HandleFunc("GET /admin/ui/users/{email}/folder-perms", s.handleUIFolderPerms)
	mux.HandleFunc("POST /admin/ui/users/{email}/folder-perms/set", s.handleUISetFolderPerm)
	mux.HandleFunc("POST /admin/ui/users/{email}/folder-perms/remove", s.handleUIRemoveFolderPerm)
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
	mux.HandleFunc("PUT /admin/ui/users/{email}/hide", s.handleUIUserHide)
	mux.HandleFunc("POST /admin/ui/users/{email}/roles/grant", s.handleUIUserGrantRole)
	mux.HandleFunc("POST /admin/ui/users/{email}/roles/revoke", s.handleUIUserRevokeRole)
	mux.HandleFunc("GET /admin/ui/syncpolicy", s.handleUISyncPolicy)
	mux.HandleFunc("PUT /admin/ui/syncpolicy", s.handleUISaveSyncPolicy)
	mux.HandleFunc("GET /admin/ui/domains", s.handleUIDomains)
	mux.HandleFunc("POST /admin/ui/domains", s.handleUICreateDomain)
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
// the credentials are valid AND the user holds at least one admin role. ok is
// false alike for wrong credentials, an unknown user, and a non-admin; err is
// set only for an infrastructure failure.
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
	r, err := s.dir.AdminRoles(id)
	if err != nil {
		return 0, nil, false, err
	}
	if len(r) == 0 {
		return 0, nil, false, nil
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
