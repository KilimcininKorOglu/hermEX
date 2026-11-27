package webmail2api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handleDirectory backs the GAL address-book autocomplete: it searches the
// directory's Global Address List and returns matching entries.
func (s *Server) handleDirectory(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
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
	// address), so the contacts page can show the GAL, not only a live search.
	if gal, ok := s.auth.(directory.GAL); ok {
		// Withhold the addresses the operator hid from the address book.
		res, err := gal.SearchGAL(c.Email, q, limit, directory.HideFromGAL)
		if err != nil {
			logError("gal-search", err, logging.Fields{"user": c.Email})
		}
		for _, e := range directory.VisibleGAL(res) {
			entries = append(entries, map[string]any{"name": e.DisplayName, "email": e.Address, "display_name": e.DisplayName})
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
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	fid, err := st.CreateFolder(nil, body.Name)
	if errors.Is(err, objectstore.ErrFolderExists) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a folder with that name already exists"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create folder"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": body.Name, "id": slugOf(fid)})
}

// handleRenameFolder renames a user folder identified by its slug. The folder
// keeps its id, so its pins and references survive the rename.
func (s *Server) handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("current")
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	id, found := userFolderByRef(st, current)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	if err := st.RenameFolder(id, nil, body.Name); err != nil {
		if errors.Is(err, objectstore.ErrBuiltinFolder) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this folder cannot be renamed"})
			return
		}
		if errors.Is(err, objectstore.ErrFolderExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a folder with that name already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not rename folder"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": body.Name, "id": slugOf(id)})
}

// handleDeleteFolder removes a user folder by its slug.
func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	st, c, ok := s.openStoreSession(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	id, found := userFolderByRef(st, name)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	if st.IsBuiltinFolder(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this folder cannot be deleted"})
		return
	}
	// Two stages, as Exchange does it: a folder is deleted by moving it under
	// Deleted Items, and deleting it AGAIN from there removes it. Which stage
	// applies is decided from the folder's stored parent chain, never from where
	// the caller believes it is.
	trash := int64(mapi.PrivateFIDDeletedItems)
	inTrash, err := st.FolderIsUnder(id, trash)
	if err != nil {
		logError("delete-folder", err, logging.Fields{"folder": name})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete folder"})
		return
	}
	if !inTrash {
		// The request names the folder by its slug; under Deleted Items it keeps
		// its own display name.
		props, err := st.GetFolderProperties(id, mapi.PrDisplayName)
		if err == nil {
			err = moveToTrash(st, id, propStr(props, mapi.PrDisplayName))
		}
		if err != nil {
			logError("delete-folder.move", err, logging.Fields{"folder": name})
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete folder"})
			return
		}
		s.unpinFolder(st, c.Mailbox, id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "movedToTrash": true})
		return
	}
	if err := st.DeleteFolder(id); err != nil {
		// A built-in folder is not the caller's to remove, and it can only be
		// reached here by name, so answer it as a refusal rather than a fault.
		if errors.Is(err, objectstore.ErrBuiltinFolder) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this folder cannot be deleted"})
			return
		}
		logError("delete-folder", err, logging.Fields{"folder": name})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete folder"})
		return
	}
	s.unpinFolder(st, c.Mailbox, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "movedToTrash": false})
}

// handleEmptyFolder removes every message from a folder: permanently from Deleted
// Items and Junk (where "empty" means discard), otherwise the messages move to
// Deleted Items. This is the old webmail's empty-folder behaviour. It resolves a
// slug (trash/spam/...) or a user folder by its id slug.
func (s *Server) handleEmptyFolder(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	fid, ok := folderFID(name)
	if !ok {
		id, found := userFolderByRef(st, name)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
			return
		}
		fid = id
	}
	trash := int64(mapi.PrivateFIDDeletedItems)
	// Emptying Trash or Junk sends each message to the Recoverable Items dumpster
	// (soft delete, recoverable until retention purges it), mirroring Exchange's
	// "empty Deleted Items" which still lands in the dumpster; emptying any other
	// folder moves its messages to Trash.
	toDumpster := fid == trash || fid == int64(mapi.PrivateFIDJunk)
	msgs, err := st.ListMessages(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot read messages"})
		return
	}
	emptied, err := emptyMessages(st, fid, msgs, toDumpster)
	if err != nil {
		logError("empty-folder", err, logging.Fields{"folder": fid, "emptied": emptied})
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "the folder could not be emptied", "emptied": emptied})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"emptied": emptied})
}

// emptyMessages removes msgs from folder fid, into the dumpster when toDumpster is
// set and into Deleted Items otherwise. It stops at the first failure and reports
// how many messages it removed before it, so the caller never reports a message
// as gone that is still in the folder.
func emptyMessages(st *objectstore.Store, fid int64, msgs []objectstore.MessageInfo, toDumpster bool) (int, error) {
	trash := int64(mapi.PrivateFIDDeletedItems)
	for i, m := range msgs {
		var err error
		if toDumpster {
			err = st.SoftDeleteMessage(fid, m.UID)
		} else {
			_, err = st.MoveMessage(fid, m.UID, trash)
		}
		if err != nil {
			return i, fmt.Errorf("remove uid %d: %w", m.UID, err)
		}
	}
	return len(msgs), nil
}

// folderByName resolves a user folder by its display name (case-sensitive).
func folderByName(st *objectstore.Store, name string) (int64, bool) {
	folders, err := st.ListFolders()
	if err != nil {
		return 0, false
	}
	parent := make(map[int64]int64, len(folders))
	for _, f := range folders {
		if f.ParentID != nil {
			parent[f.ID] = *f.ParentID
		}
	}
	// A name is not unique across the tree: a deleted folder keeps its name
	// under Deleted Items while a new one of that name lives elsewhere. The live
	// folder is the one the user means, so it wins over a deleted one.
	var deleted int64
	for _, f := range folders {
		if f.DisplayName != name {
			continue
		}
		if !underTrash(parent, f.ID) {
			return f.ID, true
		}
		if deleted == 0 {
			deleted = f.ID
		}
	}
	return deleted, deleted != 0
}

// moveToTrash moves a folder under Deleted Items. Deleted Items may already hold
// a folder of its name, and siblings cannot share one ([MS-OXCFOLD] 3.2.5.2), so
// the folder then takes the first free "name (n)".
func moveToTrash(st *objectstore.Store, id int64, name string) error {
	trash := int64(mapi.PrivateFIDDeletedItems)
	err := st.RenameFolder(id, &trash, name)
	for n := 2; errors.Is(err, objectstore.ErrFolderExists) && n <= maxTrashNameTries; n++ {
		err = st.RenameFolder(id, &trash, fmt.Sprintf("%s (%d)", name, n))
	}
	return err
}

// maxTrashNameTries bounds the "name (n)" search of moveToTrash.
const maxTrashNameTries = 1000

// underTrash reports whether a folder sits below Deleted Items in the parent map
// built from a folder listing.
func underTrash(parent map[int64]int64, fid int64) bool {
	trash := int64(mapi.PrivateFIDDeletedItems)
	for p, ok := parent[fid]; ok; p, ok = parent[p] {
		if p == trash {
			return true
		}
	}
	return false
}

// resolveFolder maps a folder identifier from the SPA to a private folder id:
// first the well-known slugs (inbox/sent/drafts/trash/spam), then a user folder
// by its id slug. This lets user folders round-trip through the list/open/action
// paths the same way built-in folders do.
func resolveFolder(st *objectstore.Store, folder string) (int64, bool) {
	if fid, ok := folderFID(folder); ok {
		return fid, true
	}
	return userFolderByRef(st, folder)
}
