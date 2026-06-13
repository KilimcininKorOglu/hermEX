package webmail

import (
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// Server is the webmail HTTP application. It authenticates against the directory
// and opens each user's mailbox store directly (in-process) per request.
// Accounts resolves recipient addresses for local delivery of composed mail.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	tmpl     *template.Template
	sessions *sessionStore
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
	mux.HandleFunc("GET /attachment", s.handleAttachment)
	mux.HandleFunc("GET /compose", s.handleComposeForm)
	mux.HandleFunc("POST /compose", s.handleComposeSubmit)
	mux.HandleFunc("GET /attachpick", s.handleAttachPick)
	mux.HandleFunc("GET /import", s.handleImportForm)
	mux.HandleFunc("POST /import", s.handleImportSubmit)
	mux.HandleFunc("GET /settings", s.handleSettingsForm)
	mux.HandleFunc("POST /settings", s.handleSettingsSubmit)
	mux.HandleFunc("POST /action", s.handleAction)
	mux.HandleFunc("POST /folder", s.handleFolder)
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
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()

	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	current := r.URL.Query().Get("folder")
	if current == "" {
		current = inboxName
	}

	// Sidebar badges: each folder's total and unread message counts.
	folderViews := buildFolderViews(folders)
	for i := range folderViews {
		if total, unread, err := st.CountMessages(folderViews[i].ID); err == nil {
			folderViews[i].Total = total
			folderViews[i].Unread = unread
		}
	}

	q := r.URL.Query()
	params := listParams{
		Sort:   whitelist(q.Get("sort"), "date", "from", "subject", "size", "flag", "read"),
		Dir:    whitelist(q.Get("dir"), "desc", "asc"),
		Filter: whitelist(q.Get("filter"), "all", "unread"),
		Page:   atoiDefault(q.Get("page"), 1),
	}
	page := mailPage{
		User:    sess.user,
		Current: current,
		Folders: folderViews,
		Field:   "all",    // search-form defaults (scoped to the current folder)
		Scope:   "folder", // until the user opens a cross-folder search
		Sort:    params.Sort,
		Dir:     params.Dir,
		Filter:  params.Filter,
		Columns: listColumns(params.Sort, params.Dir),
	}
	if id, found := resolveFolder(folders, current); found {
		if res, err := listFolderPage(st, id, current, params); err == nil {
			page.Messages = res.Messages
			page.Page = res.Page
			page.MaxPage = res.MaxPage
			page.PrevPage = res.PrevPage
			page.NextPage = res.NextPage
			page.Total = res.Total
			page.Unread = res.Unread
		}
	}
	s.render(w, "mail", page)
}

// render executes a named template, reporting a 500 on failure.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
