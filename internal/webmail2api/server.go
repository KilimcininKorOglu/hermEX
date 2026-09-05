package webmail2api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"hermex/internal/authlimit"
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
	"hermex/internal/serve"
)

// Authenticator validates webmail credentials and returns the caller's mailbox
// store path. SQLDirectory satisfies it.
type Authenticator interface {
	Authenticate(user, password string) (mailboxPath string, ok bool)
}

const (
	sessionCookie = "hermex_session"
	sessionTTL    = 12 * time.Hour
)

// Server hosts the webmail2 SPA and its /api/v1 JSON API.
type Server struct {
	auth     Authenticator
	accounts directory.Accounts // recipient resolution for outbound delivery
	spool    *relay.Spool       // external-recipient relay queue the MTA drains
	hostname string             // for outgoing Message-ID
	secret   []byte
	dist     http.Handler       // serves the built SPA with index.html fallback (nil if unset)
	secure   bool               // mark the session cookie Secure (served behind HTTPS)
	limiter  *authlimit.Limiter // failed-login throttle keyed by account

	// Pub serves per-domain public folders; nil disables the feature (the
	// endpoints then return an empty set). Set by the cmd after NewServer.
	Pub *publicfolder.Service

	// DigestSecret verifies quarantine-digest release tokens (the MTA mints them
	// with the same secret); empty disables the release page. Set after NewServer.
	DigestSecret []byte

	// Web-push delivery. pushHTTP is the SSRF-guarded client built once on first
	// use; pushAllowInternal disables the address-range block, which only a test
	// pointing at a loopback stub needs (a real push service is public).
	pushOnce          sync.Once
	pushHTTP          *http.Client
	pushAllowInternal bool

	// settingsLocks serializes the read-modify-write of each mailbox's shared
	// settings blob (PrWebmailSettings), keyed by mailbox path. Two concurrent PUTs
	// on different keys would otherwise each read the same starting blob and the
	// last writer would drop the other's change; the per-mailbox lock makes the
	// read-modify-write atomic within this process.
	settingsLocks sync.Map // mailbox path -> *sync.Mutex
}

// NewServer builds the API server. accounts and spool back outbound mail
// (oxcmail.Export → DeliverAndRelay); distDir, when set, is the filesystem path to
// the built SPA served for all non-API routes; secure marks the session cookie
// Secure (set when the front door is HTTPS).
func NewServer(auth Authenticator, accounts directory.Accounts, spool *relay.Spool, hostname string, secret []byte, distDir string, secure bool) *Server {
	s := &Server{auth: auth, accounts: accounts, spool: spool, hostname: hostname, secret: secret, secure: secure, limiter: authlimit.New(0, 0, 0)}
	if distDir != "" {
		s.dist = spaHandler(distDir)
	}
	return s
}

// Limiter exposes the failed-login limiter so the daemon can apply the operator's
// stored lockout tuning to it without a restart.
func (s *Server) Limiter() *authlimit.Limiter { return s.limiter }

// Handler routes the JSON API under /api/v1 and serves the SPA for everything
// else. Endpoints not yet implemented fall through to a benign stub so the SPA
// keeps rendering while the backend is built out.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("GET /api/v1/groups", s.handleGroups)
	mux.HandleFunc("GET /api/v1/groups/members", s.handleGroupMembers)
	mux.HandleFunc("PUT /api/v1/groups/members", s.handleGroupSetMembers)
	mux.HandleFunc("GET /api/v1/mail/message", s.handleMailMessage)
	mux.HandleFunc("POST /api/v1/mail/send", s.handleMailSend)
	mux.HandleFunc("POST /api/v1/mail/build", s.handleMailBuild)
	mux.HandleFunc("POST /api/v1/mail/send-raw", s.handleMailSendRaw)
	mux.HandleFunc("POST /api/v1/mail/draft", s.handleMailDraft)
	mux.HandleFunc("POST /api/v1/mail/flag", s.handleMailFlag)
	mux.HandleFunc("POST /api/v1/mail/followup", s.handleMailFollowup)
	mux.HandleFunc("POST /api/v1/mail/move", s.handleMailMove)
	mux.HandleFunc("POST /api/v1/mail/copy", s.handleMailCopy)
	mux.HandleFunc("POST /api/v1/mail/recall", s.handleRecall)
	mux.HandleFunc("DELETE /api/v1/mail/delete", s.handleMailDelete)
	mux.HandleFunc("GET /api/v1/mail/attachment", s.handleAttachment)
	mux.HandleFunc("GET /api/v1/mail/export", s.handleExport)
	mux.HandleFunc("GET /api/v1/mail/export-zip", s.handleExportBulk)
	mux.HandleFunc("POST /api/v1/mail/recover", s.handleRecover)
	mux.HandleFunc("GET /api/v1/mail/recoverable", s.handleRecoverableList)
	mux.HandleFunc("POST /api/v1/mail/recoverable/recover", s.handleRecoverableRecover)
	mux.HandleFunc("POST /api/v1/mail/recoverable/purge", s.handleRecoverablePurge)
	mux.HandleFunc("POST /api/v1/mail/labels", s.handleLabels)
	mux.HandleFunc("POST /api/v1/mail/mark-all-read", s.handleMarkAllRead)
	mux.HandleFunc("GET /api/v1/mail/source", s.handleSource)
	mux.HandleFunc("GET /api/v1/mail/headers", s.handleHeaders)
	mux.HandleFunc("GET /api/v1/mail/attachments-zip", s.handleAttachmentsZip)
	mux.HandleFunc("POST /api/v1/mail/import", s.handleImport)

	// Quarantine-digest release link (unauthenticated; the signed token is the
	// credential). GET confirms, POST releases, defeating email link prefetch.
	mux.HandleFunc("GET /quarantine/release", s.handleQuarantineForm)
	mux.HandleFunc("POST /quarantine/release", s.handleQuarantineRelease)
	mux.HandleFunc("GET /api/v1/mail/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("GET /api/v1/mail/invite", s.handleInvite)
	mux.HandleFunc("GET /api/v1/mail/export-ics", s.handleExportICS)
	mux.HandleFunc("POST /api/v1/mail/rsvp", s.handleRSVP)
	mux.HandleFunc("POST /api/v1/mail/propose-time", s.handleProposeTime)
	mux.HandleFunc("GET /api/v1/mail/{folder}", s.handleMailFolder)

	// Search & threads.
	mux.HandleFunc("GET /api/v1/search", s.handleSearch)
	mux.HandleFunc("GET /api/v1/threads", s.handleThreads)

	// Settings, profile, preferences, signatures, templates, categories.
	mux.HandleFunc("GET /api/v1/profile", s.handleGetProfile)
	mux.HandleFunc("PUT /api/v1/profile", s.handlePutProfile)
	mux.HandleFunc("POST /api/v1/account/password", s.handleChangePassword)
	mux.HandleFunc("GET /api/v1/preferences", s.handleGetPreferences)
	mux.HandleFunc("PUT /api/v1/preferences", s.handlePutPreferences)
	mux.HandleFunc("GET /api/v1/categories", s.handleGetCategories)
	mux.HandleFunc("PUT /api/v1/categories", s.handlePutCategories)
	mux.HandleFunc("GET /api/v1/safe-senders", s.handleGetSafeSenders)
	mux.HandleFunc("PUT /api/v1/safe-senders", s.handlePutSafeSenders)
	mux.HandleFunc("GET /api/v1/recipient-rules", s.handleGetRecipientRules)
	mux.HandleFunc("POST /api/v1/recipient-rules", s.handlePostRecipientRule)
	mux.HandleFunc("DELETE /api/v1/recipient-rules", s.handleDeleteRecipientRule)
	mux.HandleFunc("GET /api/v1/signatures", s.handleGetSignatures)
	mux.HandleFunc("POST /api/v1/signatures", s.handlePostSignature)
	mux.HandleFunc("DELETE /api/v1/signatures", s.handleDeleteSignature)
	mux.HandleFunc("GET /api/v1/signature", s.handleGetSignature)
	mux.HandleFunc("PUT /api/v1/signature", s.handlePostSignature)
	mux.HandleFunc("GET /api/v1/templates", s.handleGetTemplates)
	mux.HandleFunc("POST /api/v1/templates", s.handlePostTemplate)
	mux.HandleFunc("DELETE /api/v1/templates", s.handleDeleteTemplate)
	mux.HandleFunc("GET /api/v1/mailboxes", s.handleGetMailboxes)
	mux.HandleFunc("GET /api/v1/mailboxes/shared", s.handleGetSharedMailboxes)
	mux.HandleFunc("GET /api/v1/mailboxes/shared-as-owner", s.handleGetSharedAsOwner)
	mux.HandleFunc("GET /api/v1/identities", s.handleGetIdentities)
	mux.HandleFunc("GET /api/v1/mailboxes/{owner}/{mailbox}/acl", s.handleGetACL)
	mux.HandleFunc("POST /api/v1/mailboxes/{owner}/{mailbox}/acl", s.handleSetACL)
	mux.HandleFunc("DELETE /api/v1/mailboxes/{owner}/{mailbox}/acl/{grantee}", s.handleDeleteACL)

	// Vacation / out-of-office.
	mux.HandleFunc("GET /api/v1/vacation", s.handleGetVacation)
	mux.HandleFunc("PUT /api/v1/vacation", s.handlePutVacation)
	mux.HandleFunc("DELETE /api/v1/vacation", s.handleDeleteVacation)

	// Directory (GAL autocomplete).
	mux.HandleFunc("GET /api/v1/directory", s.handleDirectory)

	// Folders.
	mux.HandleFunc("POST /api/v1/folders", s.handleCreateFolder)
	mux.HandleFunc("PUT /api/v1/folders/{current}", s.handleRenameFolder)
	mux.HandleFunc("DELETE /api/v1/folders/{name}", s.handleDeleteFolder)
	mux.HandleFunc("POST /api/v1/folders/{name}/empty", s.handleEmptyFolder)
	mux.HandleFunc("GET /api/v1/favorites", s.handleGetFavorites)
	mux.HandleFunc("POST /api/v1/favorites/toggle", s.handleToggleFavorite)

	// Contacts.
	mux.HandleFunc("GET /api/v1/contacts", s.handleGetContacts)
	mux.HandleFunc("POST /api/v1/contacts", s.handleCreateContact)
	mux.HandleFunc("PUT /api/v1/contacts/{id}", s.handleUpdateContact)
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", s.handleDeleteContact)
	mux.HandleFunc("GET /api/v1/contacts/{id}/photo", s.handleGetContactPhoto)
	mux.HandleFunc("PUT /api/v1/contacts/{id}/photo", s.handleSetContactPhoto)
	mux.HandleFunc("DELETE /api/v1/contacts/{id}/photo", s.handleDeleteContactPhoto)
	mux.HandleFunc("GET /api/v1/contacts/{id}/vcard", s.handleExportContact)
	mux.HandleFunc("GET /api/v1/contacts/{id}/expand", s.handleExpandDistList)

	// Calendar.
	mux.HandleFunc("GET /api/v1/calendar/events", s.handleGetEvents)
	mux.HandleFunc("POST /api/v1/calendar/events", s.handleCreateEvent)
	mux.HandleFunc("PUT /api/v1/calendar/events/{uid}", s.handleUpdateEvent)
	mux.HandleFunc("DELETE /api/v1/calendar/events/{uid}", s.handleDeleteEvent)
	mux.HandleFunc("GET /api/v1/calendar/events/{uid}/ics", s.handleExportEvent)
	mux.HandleFunc("GET /api/v1/calendar/calendars", s.handleGetCalendars)
	mux.HandleFunc("POST /api/v1/calendar/calendars", s.handleCreateCalendar)
	mux.HandleFunc("PATCH /api/v1/calendar/calendars/{id}", s.handleUpdateCalendar)
	mux.HandleFunc("DELETE /api/v1/calendar/calendars/{id}", s.handleDeleteCalendar)
	mux.HandleFunc("GET /api/v1/calendar/freebusy", s.handleFreeBusy)
	mux.HandleFunc("GET /api/v1/calendar/settings", s.handleGetCalendarSettings)
	mux.HandleFunc("PUT /api/v1/calendar/settings", s.handlePutCalendarSettings)
	mux.HandleFunc("GET /api/v1/settings/appearance", s.handleGetAppearanceSettings)
	mux.HandleFunc("PUT /api/v1/settings/appearance", s.handlePutAppearanceSettings)
	mux.HandleFunc("POST /api/v1/settings/reset", s.handleResetSettings)
	mux.HandleFunc("GET /api/v1/rooms", s.handleRooms)
	mux.HandleFunc("GET /api/v1/push/vapid-public-key", s.handlePushVapidKey)
	mux.HandleFunc("POST /api/v1/push/subscribe", s.handlePushSubscribe)
	mux.HandleFunc("DELETE /api/v1/push/unsubscribe", s.handlePushUnsubscribe)
	mux.HandleFunc("GET /api/v1/push/subscriptions", s.handlePushSubscriptions)

	// Tasks & notes.
	mux.HandleFunc("GET /api/v1/tasks", s.handleGetTasks)
	mux.HandleFunc("POST /api/v1/tasks", s.handleCreateTask)
	mux.HandleFunc("PUT /api/v1/tasks/{uid}", s.handleUpdateTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{uid}", s.handleDeleteTask)
	mux.HandleFunc("GET /api/v1/reminders", s.handleGetReminders)
	mux.HandleFunc("GET /api/v1/today", s.handleGetToday)
	mux.HandleFunc("POST /api/v1/reminders/{id}/dismiss", s.handleDismissReminder)
	mux.HandleFunc("POST /api/v1/reminders/{id}/snooze", s.handleSnoozeReminder)
	mux.HandleFunc("GET /api/v1/notes", s.handleGetNotes)
	mux.HandleFunc("GET /api/v1/mail/notes", s.handleGetMailNotes)
	mux.HandleFunc("POST /api/v1/mail/notes", s.handleCreateMailNote)
	mux.HandleFunc("POST /api/v1/notes", s.handleCreateNote)
	mux.HandleFunc("PUT /api/v1/notes/{id}", s.handleUpdateNote)
	mux.HandleFunc("DELETE /api/v1/notes/{id}", s.handleDeleteNote)

	// Account-level reads (empty/default until backed).
	mux.HandleFunc("GET /api/v1/sessions", s.handleSessions)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", s.handleSessionRevoke)
	mux.HandleFunc("GET /api/v1/delegations", s.handleGetDelegations)
	mux.HandleFunc("POST /api/v1/delegations", s.handlePostDelegation)
	mux.HandleFunc("DELETE /api/v1/delegations/{id}", s.handleDeleteDelegation)
	mux.HandleFunc("GET /api/v1/scheduled", s.handleScheduled)
	mux.HandleFunc("POST /api/v1/scheduled/cancel", s.handleCancelScheduled)
	mux.HandleFunc("GET /api/v1/search-folders", s.handleGetSearchFolders)
	mux.HandleFunc("POST /api/v1/search-folders", s.handlePostSearchFolder)
	mux.HandleFunc("PUT /api/v1/search-folders/{id}", s.handlePutSearchFolder)
	mux.HandleFunc("DELETE /api/v1/search-folders/{id}", s.handleDeleteSearchFolder)
	mux.HandleFunc("GET /api/v1/search-folders/{id}/results", s.handleSearchFolderResults)

	// Public folders (per-domain, read-only browser).
	mux.HandleFunc("GET /api/v1/public-folders", s.handleGetPublicFolders)
	mux.HandleFunc("GET /api/v1/public-folders/{fid}/messages", s.handlePublicFolderMessages)
	mux.HandleFunc("GET /api/v1/public-message", s.handlePublicMessage)
	mux.HandleFunc("GET /api/v1/filters", s.handleGetFilters)
	mux.HandleFunc("POST /api/v1/filters", s.handlePostFilter)
	mux.HandleFunc("POST /api/v1/filters/reorder", s.handleReorderFilters)
	mux.HandleFunc("POST /api/v1/filters/run", s.handleRunFilters)
	mux.HandleFunc("PUT /api/v1/filters/{id}", s.handlePutFilter)
	mux.HandleFunc("DELETE /api/v1/filters/{id}", s.handleDeleteFilter)
	mux.HandleFunc("GET /api/v1/smime/certificate", s.handleGetSmimeCert)
	mux.HandleFunc("POST /api/v1/smime/certificate", s.handleUploadSmimeCert)
	mux.HandleFunc("DELETE /api/v1/smime/certificate", s.handleDeleteSmimeCert)
	mux.HandleFunc("GET /api/v1/smime/recipient", s.handleRecipientCert)
	mux.HandleFunc("POST /api/v1/smime/verify-signer", s.handleVerifySmimeSigner)
	mux.HandleFunc("GET /api/v1/branding", s.handleBranding)
	mux.HandleFunc("GET /api/v1/avatar", s.handleGetAvatar)
	mux.HandleFunc("PUT /api/v1/profile/avatar", s.handlePutAvatar)
	mux.HandleFunc("DELETE /api/v1/profile/avatar", s.handleDeleteAvatar)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)

	// Everything else under the API is not implemented yet: return a benign empty
	// body (logged) so the SPA degrades instead of hard-failing during the port.
	mux.HandleFunc("/api/v1/", s.handleStub)
	if s.dist != nil {
		mux.Handle("/", s.dist)
	}
	return securityHeaders(noStoreAPI(boundBody(s.gateForcedPasswordChange(mux))))
}

// noStoreAPI marks every API response uncacheable. Nearly all of them carry the
// caller's mail: a body, an attachment, a raw .eml, a header block, a zip. Without
// a directive the browser is free to keep any of that in its disk cache and its
// back/forward cache, so a later user of the same profile, or anyone recovering
// the disk, can read mail from a session that ended.
//
// It sets the header before the handler runs, so a handler with a deliberate
// caching policy (the avatar's short private cache) still overrides it, and it
// covers the API only: the SPA's own static assets keep the long-lived caching
// that makes them worth serving from a bundle.
func noStoreAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders stamps clickjacking and MIME-sniffing defences on every
// response. X-Frame-Options: DENY and the frame-ancestors CSP both forbid
// embedding the webmail (SPA or any endpoint) in an attacker's iframe: belt and
// braces, since older browsers honour only the former and modern ones only the
// latter. X-Content-Type-Options: nosniff stops a browser from second-guessing a
// declared Content-Type, which matters most for attachment downloads served from
// this same origin.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// forcedChangeAllowed reports whether a path stays reachable while the session user
// must change their password: the auth probes and the change endpoint itself, so
// the user can log in, read their state, change the password, and log out.
func forcedChangeAllowed(path string) bool {
	switch path {
	case "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/me", "/api/v1/account/password":
		return true
	}
	return false
}

// mustChangePassword reports whether the session user is flagged for a forced
// password change. A directory that cannot report it (no GetUser capability) never
// gates, so the API degrades open rather than locking every caller out.
func (s *Server) mustChangePassword(email string) bool {
	rd, ok := s.auth.(userLocaleReader)
	if !ok {
		return false
	}
	u, found, err := rd.GetUser(email)
	return err == nil && found && u.MustChangePassword
}

// gateForcedPasswordChange refuses every API call except the remediation allowlist
// while the session user must change their password. Without it the SPA's redirect
// to the forced-change screen would be cosmetic: a flagged session could call the
// data API directly with its cookie and bypass the change. Unauthenticated and
// non-API requests pass through untouched (the handlers enforce their own auth).
func (s *Server) gateForcedPasswordChange(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && !forcedChangeAllowed(r.URL.Path) {
			if c, ok := s.session(r); ok && s.mustChangePassword(c.Email) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "password change required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// webLoginAllowed reports whether the account may sign in to webmail, from the
// web service privilege the admin panel exposes per user. Every other transport
// already honours its own privilege (IMAP/POP3, SMTP submission, ActiveSync,
// DAV), so without this the panel's Web switch was the one that did nothing.
// A directory that does not expose privileges admits the login, matching
// passwordChangeAllowed.
func (s *Server) webLoginAllowed(user string) bool {
	pr, ok := s.auth.(interface {
		Privileges(string) (directory.ServicePrivileges, bool)
	})
	if !ok {
		return true
	}
	privs, _ := pr.Privileges(user)
	return privs.Web
}

// authenticateForChange verifies credentials for the webmail2 remediation flow
// (login + current-password check), admitting an account that must change its
// password so it can reach the forced-change screen. It falls back to the strict
// Authenticate when the directory does not expose the lenient capability.
func (s *Server) authenticateForChange(user, password string) (string, bool) {
	if a, ok := s.auth.(interface {
		AuthenticateAllowingPasswordChange(user, password string) (string, bool)
	}); ok {
		return a.AuthenticateAllowingPasswordChange(user, password)
	}
	return s.auth.Authenticate(user, password)
}

// decodeJSON decodes the request body into v.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// openStore authenticates the request and opens the caller's mailbox store,
// writing the error response and reporting false on failure. The caller closes
// the returned store.
func (s *Server) openStore(w http.ResponseWriter, r *http.Request) (*objectstore.Store, sessionClaims, bool) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, sessionClaims{}, false
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		logError("open-store", err, logging.Fields{"user": c.Email})
		// The client is told only that the mailbox is unavailable, so this line is
		// the operator's only trace of why. A store that will not open is an
		// infrastructure fault (a corrupt database, a failing disk, a held lock),
		// exactly the class that cannot be diagnosed from the status alone.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return nil, sessionClaims{}, false
	}
	return st, c, true
}

// session reads and verifies the session cookie.
func (s *Server) session(r *http.Request) (sessionClaims, bool) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return sessionClaims{}, false
	}
	c, err := verifyToken(s.secret, ck.Value, time.Now())
	if err != nil {
		return sessionClaims{}, false
	}
	if !s.sessionActive(c) {
		return sessionClaims{}, false
	}
	// Webmail authenticates with a cookie, so the access log has no Basic header to
	// read; report the account here, where every authenticated handler passes.
	serve.SetUser(r, c.Email)
	return c, true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	// A login carries no session yet, so the access log would otherwise record the
	// one request that matters most to an auditor, a failed sign-in, with no account
	// at all. Reported from the claimed address, before it is verified.
	serve.SetUser(r, req.Email)
	// Throttle online guessing on both axes. The account axis blunts
	// credential-stuffing against one mailbox; the address axis blunts one host
	// spraying a password across the whole directory, which an account axis alone
	// never sees. The address is the forwarded client, not the raw connection:
	// behind the gateway every request otherwise carries the same proxy address.
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, req.Email) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
		return
	}
	// Login admits an account that must change its password so it can reach the
	// forced-change screen; the per-request gate then confines it to the
	// remediation allowlist until the change clears the flag.
	mbox, ok := s.authenticateForChange(req.Email, req.Password)
	if !ok {
		s.limiter.Fail(addr, req.Email)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	s.limiter.Succeed(addr, req.Email)
	// The credentials are right; the account may still be barred from this service.
	// Checked here, after the throttle is cleared, so a correct password is never
	// counted as a guess, and before any token is minted or session recorded, so a
	// barred account leaves nothing behind. 403 rather than 401: the identity is
	// established, the authorization is not.
	if !s.webLoginAllowed(req.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "webmail access is disabled for this account"})
		return
	}
	now := time.Now()
	exp := now.Add(sessionTTL)
	jti, err := newJTI()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	tok, err := mintToken(s.secret, sessionClaims{Email: req.Email, Mailbox: mbox, Jti: jti, Exp: exp.Unix()})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Record the session so the user can list and revoke it; best-effort, so a store
	// error is logged but never fails the login (the token still authenticates).
	if err := s.recordLoginSession(r, req.Email, jti, now, exp); err != nil {
		logError("record-login-session", err, logging.Fields{"user": req.Email})
	}
	// #nosec G124 -- Secure tracks the deployment: set whenever the server serves TLS, cleared only for a plain-HTTP dev run
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
	writeJSON(w, http.StatusOK, map[string]any{"expiresIn": int(sessionTTL.Seconds())})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke the server-side session so the cleared token cannot be replayed.
	if c, ok := s.session(r); ok {
		s.revokeCurrentSession(c)
	}
	// #nosec G124 -- Secure tracks the deployment: set whenever the server serves TLS, cleared only for a plain-HTTP dev run
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMe is the always-200 session probe the SPA calls on load.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	hasAvatar, onboarded := false, false
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		if photo, _ := st.UserPhoto(); photo != nil {
			hasAvatar = true
		}
		onboarded = onboardedFlag(st)
		_ = st.Close()
	}
	// Timezone + locale come from the directory so the SPA restores the chosen
	// zone on reload instead of clearing it; empty means follow-the-device.
	// must_change_password gates the SPA into a forced password-change screen.
	timezone, locale, mustChange := "", "", false
	if rd, ok := s.auth.(userLocaleReader); ok {
		if u, found, err := rd.GetUser(c.Email); err == nil && found {
			timezone, locale, mustChange = u.Timezone, u.Lang, u.MustChangePassword
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":        true,
		"email":                c.Email,
		"isAdmin":              false,
		"has_avatar":           hasAvatar,
		"onboarded":            onboarded,
		"timezone":             timezone,
		"locale":               locale,
		"must_change_password": mustChange,
	})
}

// mailJSON is the SPA's Mail shape (camelCase) for a folder-listing row.
type mailJSON struct {
	ID             string `json:"id"`
	From           string `json:"from"`
	FromName       string `json:"fromName"`
	Subject        string `json:"subject"`
	Preview        string `json:"preview"`
	Date           string `json:"date"`
	Read           bool   `json:"read"`
	Starred        bool   `json:"starred"`
	Folder         string `json:"folder"`
	HasAttachments bool   `json:"hasAttachments"`
	Size           int    `json:"size"`
}

// folderFID maps the SPA's folder slugs to well-known private folder ids.
func folderFID(slug string) (int64, bool) {
	switch slug {
	case "inbox":
		return mapi.PrivateFIDInbox, true
	case "sent":
		return mapi.PrivateFIDSentItems, true
	case "drafts":
		return mapi.PrivateFIDDraft, true
	case "trash":
		return mapi.PrivateFIDDeletedItems, true
	case "spam", "junk":
		return mapi.PrivateFIDJunk, true
	default:
		return 0, false
	}
}

func (s *Server) handleMailFolder(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer mb.st.Close()
	folder := r.PathValue("folder")
	fid, ok := resolveFolder(mb.st, folder)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"emails": []mailJSON{}})
		return
	}
	if !mb.readAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	// Filter, sort and paginate in the query, so a page turn costs the page and
	// not the folder: reading every row and sorting it in memory made an inbox
	// request scale with how long the account had existed. The SPA receives one
	// already-ordered page plus the matching total (for the pager) and the
	// folder's unread count (for the badge).
	q := r.URL.Query()
	opt := objectstore.ListOptions{
		Unread:  q.Get("filter") == "unread",
		Flagged: q.Get("filter") == "starred",
		Sort:    objectstore.SortKey(q.Get("sort")),
		Desc:    q.Get("dir") != "asc",
	}
	// Pagination is opt-in: a caller that sends no pageSize (e.g. the sent/drafts
	// pages) still gets the whole sorted/filtered folder.
	if ps := q.Get("pageSize"); ps != "" {
		opt.Limit = atoiOr(ps, 50)
		if opt.Limit < 1 || opt.Limit > 200 {
			opt.Limit = 50
		}
		opt.Offset = max(atoiOr(q.Get("page"), 0), 0) * opt.Limit
	}
	page, err := mb.st.ListMessagesPage(fid, opt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	emails := make([]mailJSON, 0, len(page.Messages))
	for _, m := range page.Messages {
		emails = append(emails, mailRow(folder, m))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"emails": emails,
		"total":  page.Total,
		"unread": page.Unread,
	})
}

// mailRow projects a stored message onto the SPA's mail row shape.
func mailRow(folder string, m objectstore.MessageInfo) mailJSON {
	return mailJSON{
		ID:       messageID(folder, m.UID),
		From:     m.Sender,
		FromName: m.Sender,
		Subject:  m.Subject,
		// Both come off the index row, so a page of rows costs no message reads.
		Preview:        m.Preview,
		HasAttachments: m.HasAttachments,
		Date:           m.InternalDate.Format(time.RFC3339),
		Read:           m.Flags&objectstore.FlagSeen != 0,
		Starred:        m.Flags&objectstore.FlagFlagged != 0,
		Folder:         folder,
		Size:           int(m.Size),
	}
}

// atoiOr parses s as an int, returning def on any parse error.
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// handleStub answers not-yet-implemented API calls with an empty body so the SPA
// degrades gracefully. The path is logged so the port can track what is still
// missing.
func (s *Server) handleStub(w http.ResponseWriter, r *http.Request) {
	// Both values go through logSafe, which drops every C0 character and DEL and
	// bounds the length. The scanner cannot follow a sanitizer that is not one of
	// the standard library's, so it is told here rather than by disabling the rule.
	// #nosec G706
	log.Printf("webmail2api: unimplemented %s %s", logSafe(r.Method), logSafe(r.URL.Path))
	writeJSON(w, http.StatusOK, map[string]any{})
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
