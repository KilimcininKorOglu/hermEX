package webmail

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
)

// RecipientRuleStore manages a user's personal allow/block rules — the ones the MTA
// applies per recipient at delivery. *directory.SQLDirectory satisfies it; it is an
// interface so the webmail Server only sees the narrow surface it needs.
type RecipientRuleStore interface {
	ListRecipientRules(username string) ([]directory.RecipientRule, error)
	SetRecipientRule(username, pattern, action string) error
	DeleteRecipientRule(username, pattern string) (bool, error)
}

// Server is the webmail HTTP application. It authenticates against the directory
// and opens each user's mailbox store directly (in-process) per request.
// Accounts resolves recipient addresses for local delivery of composed mail.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	tmpl     *template.Template
	sessions *sessionStore
	Logger   *logging.Logger       // central activity log; nil disables logging
	Spool    *relay.Spool          // outbound relay queue; nil sends local-only
	Pub      *publicfolder.Service // per-domain public folders; nil disables them
	// DigestSecret verifies quarantine-digest release tokens (the MTA mints them with
	// the same key). Empty disables the release endpoint — links 404.
	DigestSecret []byte
	// Rules manages the user's personal allow/block rules from the settings page; nil
	// hides the section (the MTA still enforces any rules already stored).
	Rules RecipientRuleStore
	// Shared enumerates the shared mailboxes the directory knows; nil hides the
	// sidebar's shared-mailboxes section. Access to each is rechecked per store, so
	// listing the directory's set never by itself grants entry.
	Shared directory.SharedMailboxLister
}

// smimeEvent logs an S/MIME crypto operation under the smime subsystem, tagged
// with the acting user. errMsg is empty on success. A nil logger is a no-op.
func (s *Server) smimeEvent(level logging.Level, user, name, errMsg string, f logging.Fields) {
	s.Logger.Emit(logging.Event{Level: level, Subsystem: logging.SMIME, Name: name, User: user, Err: errMsg, Fields: f})
}

// NewServer builds a webmail server, compiling the embedded templates.
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{auth: auth, accounts: accounts, hostname: hostname, tmpl: tmpl, sessions: newSessionStore()}, nil
}

// Handler returns the HTTP handler serving the webmail application.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("GET /logout", s.handleLogout)
	mux.HandleFunc("GET /mail", s.handleMail)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /message", s.handleMessage)
	mux.HandleFunc("GET /print", s.handlePrint)
	mux.HandleFunc("GET /eml", s.handleEML)
	mux.HandleFunc("GET /attachment", s.handleAttachment)
	mux.HandleFunc("GET /compose", s.handleComposeForm)
	mux.HandleFunc("POST /compose", s.handleComposeSubmit)
	mux.HandleFunc("GET /resolve", s.handleResolve)
	// Quarantine digest release: unauthenticated — the signed token in the link is the
	// sole credential. GET confirms (so link prefetch never releases), POST releases.
	mux.HandleFunc("GET /quarantine/release", s.handleQuarantineReleaseForm)
	mux.HandleFunc("POST /quarantine/release", s.handleQuarantineRelease)
	mux.HandleFunc("GET /attachpick", s.handleAttachPick)
	mux.HandleFunc("GET /import", s.handleImportForm)
	mux.HandleFunc("POST /import", s.handleImportSubmit)
	mux.HandleFunc("GET /settings", s.handleSettingsForm)
	mux.HandleFunc("POST /settings", s.handleSettingsSubmit)
	mux.HandleFunc("GET /rules", s.handleRulesForm)
	mux.HandleFunc("POST /rules", s.handleRulesSubmit)
	mux.HandleFunc("GET /oof", s.handleOOFForm)
	mux.HandleFunc("POST /oof", s.handleOOFSubmit)
	mux.HandleFunc("GET /password", s.handlePasswordForm)
	mux.HandleFunc("POST /password", s.handlePasswordSubmit)
	mux.HandleFunc("GET /smime", s.handleSmimeForm)
	mux.HandleFunc("POST /smime", s.handleSmimeSubmit)
	mux.HandleFunc("POST /action", s.handleAction)
	mux.HandleFunc("POST /bulk", s.handleBulk)
	mux.HandleFunc("POST /export", s.handleExport)
	mux.HandleFunc("POST /folder", s.handleFolder)
	mux.HandleFunc("GET /public-folders", s.handlePublicFolders)
	mux.HandleFunc("GET /public-message", s.handlePublicMessage)
	mux.HandleFunc("GET /public-attachment", s.handlePublicAttachment)
	mux.HandleFunc("GET /{$}", s.handleRoot)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFrom(r); ok {
		http.Redirect(w, r, "/mail", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFrom(r); ok {
		http.Redirect(w, r, "/mail", http.StatusSeeOther)
		return
	}
	s.render(w, "login", map[string]any{})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("user")
	pass := r.FormValue("password")
	path, ok := s.auth.Authenticate(user, pass)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]any{"Error": "Invalid email or password.", "User": user})
		return
	}
	if privs, _ := s.auth.Privileges(user); !privs.Web {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, "login", map[string]any{"Error": "Webmail access is disabled for this account.", "User": user})
		return
	}
	token := s.sessions.create(user, path)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	http.Redirect(w, r, "/mail", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.destroy(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleMail(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// The sidebar folder tree, badges, and preferences always reflect the user's
	// OWN mailbox, even while a shared mailbox's messages fill the list.
	ownSt, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer ownSt.Close()

	ownFolders, err := ownSt.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	current := r.URL.Query().Get("folder")
	if current == "" {
		current = inboxName
	}

	// Sidebar badges: each folder's total and unread message counts.
	folderViews := buildFolderViews(ownFolders)
	for i := range folderViews {
		if total, unread, err := ownSt.CountMessages(folderViews[i].ID); err == nil {
			folderViews[i].Total = total
			folderViews[i].Unread = unread
		}
	}

	// Saved preferences supply the list defaults; a URL parameter overrides them.
	cfg, err := loadSettings(ownSt)
	if err != nil {
		cfg = defaultSettings()
	}

	// The message list reflects either the own mailbox or a shared mailbox the user
	// selected (?mbox), validated and access-checked server-side. A shared folder is
	// read-gated per FrightsReadAny so a delegate sees only what they may read.
	contentSt, contentFolders := ownSt, ownFolders
	var mbox string
	if sel := mboxParam(r); sel != "" {
		sh, addr, ok := s.openSharedFor(sess, sel)
		if !ok {
			http.NotFound(w, r)
			return
		}
		defer sh.Close()
		contentSt = sh
		if contentFolders, err = sh.ListFolders(); err != nil {
			http.Error(w, "cannot read folders", http.StatusInternalServerError)
			return
		}
		mbox = addr
	}

	q := r.URL.Query()
	params := listParams{
		Sort:         whitelist(orDefault(q.Get("sort"), cfg.DefaultSort), "date", "from", "subject", "size", "flag", "read"),
		Dir:          whitelist(orDefault(q.Get("dir"), cfg.DefaultDir), "desc", "asc"),
		Filter:       whitelist(q.Get("filter"), "all", "unread"),
		Page:         atoiDefault(q.Get("page"), 1),
		Conversation: cfg.ConversationView,
	}
	page := mailPage{
		User:            sess.user,
		Current:         current,
		Folders:         folderViews,
		Field:           "all",    // search-form defaults (scoped to the current folder)
		Scope:           "folder", // until the user opens a cross-folder search
		Sort:            params.Sort,
		Dir:             params.Dir,
		Filter:          params.Filter,
		Density:         whitelist(orDefault(q.Get("density"), cfg.Density), "compact", "extended"),
		Columns:         listColumns(params.Sort, params.Dir),
		Categories:      cfg.Categories,
		PreviewPane:     whitelist(orDefault(q.Get("preview"), cfg.PreviewPane), "none", "right", "bottom"),
		Conversation:    params.Conversation,
		PublicFolders:   s.listVisiblePublicFolders(sess),
		SharedMailboxes: s.listAccessibleSharedMailboxes(sess),
		Mbox:            mbox,
	}
	// A shared folder is read-only unless the caller holds edit/delete rights on it;
	// the own mailbox is always writable.
	readOnly := mbox != ""
	if id, found := resolveFolder(contentFolders, current); found {
		// A shared folder must grant the caller read access; the own mailbox needs no
		// per-folder check (the user owns it).
		if mbox != "" {
			rights, err := contentSt.ResolvePermission(id, sess.user)
			if err != nil || rights&mapi.FrightsReadAny == 0 {
				http.NotFound(w, r)
				return
			}
			readOnly = rights&(mapi.FrightsEditAny|mapi.FrightsDeleteAny) == 0
		}
		if res, err := listFolderPage(contentSt, id, current, params, cfg.Categories); err == nil {
			page.Messages = res.Messages
			page.Threads = res.Threads
			page.Page = res.Page
			page.MaxPage = res.MaxPage
			page.PrevPage = res.PrevPage
			page.NextPage = res.NextPage
			page.Total = res.Total
			page.Unread = res.Unread
		}
	}
	page.ReadOnly = readOnly
	page.MoveTargets = buildFolderViews(contentFolders)
	// Carry the shared-mailbox selector onto every list row so the reader and action
	// links stay in the shared context; a shared folder the caller cannot modify
	// renders its rows read-only (per-row write controls hidden).
	if mbox != "" {
		for i := range page.Messages {
			page.Messages[i].Mbox = mbox
			page.Messages[i].ReadOnly = readOnly
		}
		for i := range page.Threads {
			for j := range page.Threads[i].Messages {
				page.Threads[i].Messages[j].Mbox = mbox
				page.Threads[i].Messages[j].ReadOnly = readOnly
			}
		}
	}
	s.render(w, "mail", page)
}

// render executes a named template, reporting a 500 on failure. It renders into a
// buffer first so a template-execution error yields a clean 500 instead of a
// half-written page followed by an error tail (the latter also logged a spurious
// double WriteHeader); only a fully rendered page is flushed to the client.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}
