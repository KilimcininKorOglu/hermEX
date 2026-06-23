package webmail2api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
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
	dist     http.Handler // serves the built SPA with index.html fallback (nil if unset)
	secure   bool         // mark the session cookie Secure (served behind HTTPS)
}

// NewServer builds the API server. accounts and spool back outbound mail
// (oxcmail.Export → DeliverAndRelay); distDir, when set, is the filesystem path to
// the built SPA served for all non-API routes; secure marks the session cookie
// Secure (set when the front door is HTTPS).
func NewServer(auth Authenticator, accounts directory.Accounts, spool *relay.Spool, hostname string, secret []byte, distDir string, secure bool) *Server {
	s := &Server{auth: auth, accounts: accounts, spool: spool, hostname: hostname, secret: secret, secure: secure}
	if distDir != "" {
		s.dist = spaHandler(distDir)
	}
	return s
}

// Handler routes the JSON API under /api/v1 and serves the SPA for everything
// else. Endpoints not yet implemented fall through to a benign stub so the SPA
// keeps rendering while the backend is built out.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("GET /api/v1/mail/message", s.handleMailMessage)
	mux.HandleFunc("POST /api/v1/mail/send", s.handleMailSend)
	mux.HandleFunc("POST /api/v1/mail/draft", s.handleMailDraft)
	mux.HandleFunc("POST /api/v1/mail/flag", s.handleMailFlag)
	mux.HandleFunc("POST /api/v1/mail/move", s.handleMailMove)
	mux.HandleFunc("DELETE /api/v1/mail/delete", s.handleMailDelete)
	mux.HandleFunc("GET /api/v1/mail/attachment", s.handleAttachment)
	mux.HandleFunc("GET /api/v1/mail/export", s.handleExport)
	mux.HandleFunc("POST /api/v1/mail/recover", s.handleRecover)
	mux.HandleFunc("POST /api/v1/mail/labels", s.handleLabels)
	mux.HandleFunc("GET /api/v1/mail/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("GET /api/v1/mail/invite", s.handleInvite)
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
	mux.HandleFunc("GET /api/v1/mailboxes/shared-as-owner", s.handleGetSharedMailboxes)
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

	// Contacts.
	mux.HandleFunc("GET /api/v1/contacts", s.handleGetContacts)
	mux.HandleFunc("POST /api/v1/contacts", s.handleCreateContact)
	mux.HandleFunc("PUT /api/v1/contacts/{id}", s.handleUpdateContact)
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", s.handleDeleteContact)

	// Calendar.
	mux.HandleFunc("GET /api/v1/calendar/events", s.handleGetEvents)
	mux.HandleFunc("POST /api/v1/calendar/events", s.handleCreateEvent)
	mux.HandleFunc("PUT /api/v1/calendar/events/{uid}", s.handleUpdateEvent)
	mux.HandleFunc("DELETE /api/v1/calendar/events/{uid}", s.handleDeleteEvent)
	mux.HandleFunc("GET /api/v1/calendar/calendars", s.handleGetCalendars)
	mux.HandleFunc("GET /api/v1/calendar/freebusy", s.handleFreeBusy)
	mux.HandleFunc("GET /api/v1/rooms", s.handleRooms)

	// Tasks & notes.
	mux.HandleFunc("GET /api/v1/tasks", s.handleGetTasks)
	mux.HandleFunc("POST /api/v1/tasks", s.handleCreateTask)
	mux.HandleFunc("PUT /api/v1/tasks/{uid}", s.handleUpdateTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{uid}", s.handleDeleteTask)
	mux.HandleFunc("GET /api/v1/notes", s.handleGetNotes)
	mux.HandleFunc("POST /api/v1/notes", s.handleCreateNote)
	mux.HandleFunc("PUT /api/v1/notes/{id}", s.handleUpdateNote)
	mux.HandleFunc("DELETE /api/v1/notes/{id}", s.handleDeleteNote)

	// Account-level reads (empty/default until backed).
	mux.HandleFunc("GET /api/v1/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/v1/delegations", s.handleGetDelegations)
	mux.HandleFunc("POST /api/v1/delegations", s.handlePostDelegation)
	mux.HandleFunc("DELETE /api/v1/delegations/{id}", s.handleDeleteDelegation)
	mux.HandleFunc("GET /api/v1/scheduled", s.handleScheduled)
	mux.HandleFunc("GET /api/v1/search-folders", s.handleSearchFolders)
	mux.HandleFunc("GET /api/v1/filters", s.handleGetFilters)
	mux.HandleFunc("POST /api/v1/filters", s.handlePostFilter)
	mux.HandleFunc("POST /api/v1/filters/reorder", s.handleReorderFilters)
	mux.HandleFunc("PUT /api/v1/filters/{id}", s.handlePutFilter)
	mux.HandleFunc("DELETE /api/v1/filters/{id}", s.handleDeleteFilter)
	mux.HandleFunc("GET /api/v1/smime/certificate", s.handleSmimeCert)
	mux.HandleFunc("GET /api/v1/branding", s.handleBranding)
	mux.HandleFunc("GET /api/v1/avatar", s.handleAvatar)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)

	// Everything else under the API is not implemented yet: return a benign empty
	// body (logged) so the SPA degrades instead of hard-failing during the port.
	mux.HandleFunc("/api/v1/", s.handleStub)
	if s.dist != nil {
		mux.Handle("/", s.dist)
	}
	return mux
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
	mbox, ok := s.auth.Authenticate(req.Email, req.Password)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	exp := time.Now().Add(sessionTTL)
	tok, err := mintToken(s.secret, sessionClaims{Email: req.Email, Mailbox: mbox, Exp: exp.Unix()})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"email":         c.Email,
		"isAdmin":       false,
		"has_avatar":    false,
		"onboarded":     true,
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
	fid, ok := folderFID(folder)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"emails": []mailJSON{}})
		return
	}
	if !mb.readAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	msgs, err := mb.st.ListMessages(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	emails := make([]mailJSON, 0, len(msgs))
	for _, m := range msgs {
		emails = append(emails, mailJSON{
			ID:       messageID(folder, m.UID),
			From:     m.Sender,
			FromName: m.Sender,
			Subject:  m.Subject,
			Date:     m.InternalDate.Format(time.RFC3339),
			Read:     m.Flags&objectstore.FlagSeen != 0,
			Starred:  m.Flags&objectstore.FlagFlagged != 0,
			Folder:   folder,
			Size:     int(m.Size),
		})
	}
	// ListMessages returns oldest-first (by uid); the SPA shows newest-first.
	for i, j := 0, len(emails)-1; i < j; i, j = i+1, j-1 {
		emails[i], emails[j] = emails[j], emails[i]
	}
	writeJSON(w, http.StatusOK, map[string]any{"emails": emails})
}

// handleStub answers not-yet-implemented API calls with an empty body so the SPA
// degrades gracefully. The path is logged so the port can track what is still
// missing.
func (s *Server) handleStub(w http.ResponseWriter, r *http.Request) {
	log.Printf("webmail2api: unimplemented %s %s", r.Method, r.URL.Path)
	writeJSON(w, http.StatusOK, map[string]any{})
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
