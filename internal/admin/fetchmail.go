package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// fetchmailInput is the JSON/form shape for creating a poll configuration; the local
// mailbox comes from the URL, not the body.
type fetchmailInput struct {
	SrcServer   string `json:"srcServer"`
	SrcPort     int    `json:"srcPort"`
	SrcUser     string `json:"srcUser"`
	SrcPassword string `json:"srcPassword"`
	Protocol    string `json:"protocol"`
	SrcFolder   string `json:"srcFolder"`
	FetchAll    bool   `json:"fetchall"`
	Keep        bool   `json:"keep"`
	UseSSL      bool   `json:"useSSL"`
	SSLVerify   bool   `json:"sslVerify"`
	Active      bool   `json:"active"`
}

// entry maps the input to a directory entry for the given mailbox.
func (in fetchmailInput) entry(mailbox string) directory.FetchmailEntry {
	return directory.FetchmailEntry{
		Mailbox:     mailbox,
		Active:      in.Active,
		SrcServer:   in.SrcServer,
		SrcPort:     in.SrcPort,
		SrcUser:     in.SrcUser,
		SrcPassword: in.SrcPassword,
		Protocol:    in.Protocol,
		SrcFolder:   in.SrcFolder,
		FetchAll:    in.FetchAll,
		Keep:        in.Keep,
		UseSSL:      in.UseSSL,
		SSLVerify:   in.SSLVerify,
	}
}

// fetchmailView is one configuration as shown to the admin. The stored source password is
// never echoed back, in the API or the page.
type fetchmailView struct {
	ID        int64  `json:"id"`
	SrcServer string `json:"srcServer"`
	SrcPort   int    `json:"srcPort"`
	SrcUser   string `json:"srcUser"`
	Protocol  string `json:"protocol"`
	SrcFolder string `json:"srcFolder"`
	FetchAll  bool   `json:"fetchall"`
	Keep      bool   `json:"keep"`
	UseSSL    bool   `json:"useSSL"`
	SSLVerify bool   `json:"sslVerify"`
	Active    bool   `json:"active"`
}

func fetchmailViews(entries []directory.FetchmailEntry) []fetchmailView {
	out := make([]fetchmailView, 0, len(entries))
	for _, e := range entries {
		out = append(out, fetchmailView{
			ID: e.ID, SrcServer: e.SrcServer, SrcPort: e.SrcPort, SrcUser: e.SrcUser,
			Protocol: e.Protocol, SrcFolder: e.SrcFolder, FetchAll: e.FetchAll,
			Keep: e.Keep, UseSSL: e.UseSSL, SSLVerify: e.SSLVerify, Active: e.Active,
		})
	}
	return out
}

// ownsFetchmail reports whether entry id belongs to the given mailbox, so a delete cannot
// reach another user's entry by id.
func (s *Server) ownsFetchmail(mailbox string, id int64) bool {
	entries, err := s.dir.ListFetchmail(mailbox)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// handleListUserFetchmail returns a user's poll configurations (passwords redacted).
func (s *Server) handleListUserFetchmail(w http.ResponseWriter, r *http.Request) {
	entries, err := s.dir.ListFetchmail(r.PathValue("email"))
	if err != nil {
		http.Error(w, "could not list fetchmail", http.StatusInternalServerError)
		return
	}
	writeJSON(w, fetchmailViews(entries))
}

// handleCreateUserFetchmail adds a poll configuration for the user.
func (s *Server) handleCreateUserFetchmail(w http.ResponseWriter, r *http.Request) {
	var in fetchmailInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateFetchmail(in.entry(r.PathValue("email")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int64{"id": id})
}

// handleDeleteUserFetchmail removes one of the user's poll configurations by id.
func (s *Server) handleDeleteUserFetchmail(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if !s.ownsFetchmail(email, id) {
		http.Error(w, "no such fetchmail entry for this user", http.StatusNotFound)
		return
	}
	if _, err := s.dir.DeleteFetchmail(id); err != nil {
		http.Error(w, "could not delete fetchmail entry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// renderFetchmailPanel re-renders the user's fetchmail list for htmx after a change.
func (s *Server) renderFetchmailPanel(w http.ResponseWriter, email, csrf, errMsg string) {
	entries, err := s.dir.ListFetchmail(email)
	if err != nil && errMsg == "" {
		errMsg = "Could not load fetchmail: " + err.Error()
	}
	s.render(w, "fetchmail-panel", map[string]any{
		"Email":     email,
		"CSRF":      csrf,
		"Fetchmail": fetchmailViews(entries),
		"Error":     errMsg,
	})
}

// handleUIUserAddFetchmail adds a configuration from the detail form and returns the
// refreshed panel.
func (s *Server) handleUIUserAddFetchmail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := r.PathValue("email")
	atoi := func(v string) int { n, _ := strconv.Atoi(v); return n }
	in := fetchmailInput{
		SrcServer:   r.PostFormValue("srcServer"),
		SrcPort:     atoi(r.PostFormValue("srcPort")),
		SrcUser:     r.PostFormValue("srcUser"),
		SrcPassword: r.PostFormValue("srcPassword"),
		Protocol:    r.PostFormValue("protocol"),
		SrcFolder:   r.PostFormValue("srcFolder"),
		FetchAll:    r.PostFormValue("fetchall") != "",
		Keep:        r.PostFormValue("keep") != "",
		UseSSL:      r.PostFormValue("useSSL") != "",
		SSLVerify:   r.PostFormValue("sslVerify") != "",
		Active:      r.PostFormValue("active") != "",
	}
	errMsg := ""
	if _, err := s.dir.CreateFetchmail(in.entry(email)); err != nil {
		errMsg = "Could not add: " + err.Error()
	}
	s.renderFetchmailPanel(w, email, csrfCookieValue(r), errMsg)
}

// handleUIUserDeleteFetchmail removes a configuration from the detail form and returns the
// refreshed panel.
func (s *Server) handleUIUserDeleteFetchmail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := r.PathValue("email")
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	errMsg := ""
	if s.ownsFetchmail(email, id) {
		if _, err := s.dir.DeleteFetchmail(id); err != nil {
			errMsg = "Could not delete: " + err.Error()
		}
	} else {
		errMsg = "No such entry."
	}
	s.renderFetchmailPanel(w, email, csrfCookieValue(r), errMsg)
}
