package webmail2api

import (
	"net/http"
	"strconv"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// handleDirectory backs the GAL address-book autocomplete: it searches the
// directory's Global Address List and returns matching entries.
func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	q := r.URL.Query().Get("q")
	limit := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	entries := []map[string]any{}
	if gal, ok := s.auth.(directory.GAL); ok && q != "" {
		if res, err := gal.SearchGAL(q, limit); err == nil {
			for _, e := range res {
				entries = append(entries, map[string]any{"name": e.DisplayName, "email": e.Address, "display_name": e.DisplayName})
			}
		}
	}
	// Return under several keys so the SPA's expected field matches regardless.
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "contacts": entries, "results": entries})
}

// handleCreateFolder makes a new top-level user folder.
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if _, err := st.CreateFolder(nil, body.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create folder"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": body.Name})
}

// handleRenameFolder renames a user folder identified by its current display name.
func (s *Server) handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("current")
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id, found := folderByName(st, current)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	if err := st.RenameFolder(id, nil, body.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not rename folder"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": body.Name})
}

// handleDeleteFolder removes a user folder by display name.
func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id, found := folderByName(st, name)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	if err := st.DeleteFolder(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete folder"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// folderByName resolves a user folder by its display name (case-sensitive).
func folderByName(st *objectstore.Store, name string) (int64, bool) {
	folders, err := st.ListFolders()
	if err != nil {
		return 0, false
	}
	for _, f := range folders {
		if f.DisplayName == name {
			return f.ID, true
		}
	}
	return 0, false
}
