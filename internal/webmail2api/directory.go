package webmail2api

import (
	"net/http"
	"strconv"

	"hermex/internal/directory"
	"hermex/internal/mapi"
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
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	entries := []map[string]any{}
	// An empty query lists the whole directory (SearchGAL("") matches every
	// address), so the contacts page can show the GAL — not only a live search.
	if gal, ok := s.auth.(directory.GAL); ok {
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

// handleEmptyFolder removes every message from a folder: permanently from Deleted
// Items and Junk (where "empty" means discard), otherwise the messages move to
// Deleted Items — the old webmail's empty-folder behaviour. It resolves a built-in
// slug (trash/spam/...) or a custom folder by display name.
func (s *Server) handleEmptyFolder(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	fid, ok := folderFID(name)
	if !ok {
		id, found := folderByName(st, name)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
			return
		}
		fid = id
	}
	trash := int64(mapi.PrivateFIDDeletedItems)
	permanent := fid == trash || fid == int64(mapi.PrivateFIDJunk)
	msgs, err := st.ListMessages(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot read messages"})
		return
	}
	for _, m := range msgs {
		if permanent {
			_ = st.DeleteMessage(fid, m.UID)
		} else {
			_, _ = st.MoveMessage(fid, m.UID, trash)
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"emptied": len(msgs)})
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
