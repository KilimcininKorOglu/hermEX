package webmail

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handleFolder applies a folder-management action (create / rename / delete) and
// redirects back to the mailbox, which re-renders the sidebar. Built-in folders
// (id < the unassigned-start id) are protected from rename/delete server-side,
// not merely by hiding the UI.
func (s *Server) handleFolder(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	// Open the own mailbox, or a shared mailbox the caller selected (?mbox),
	// validated and access-checked server-side.
	mbox := mboxParam(r)
	var st *objectstore.Store
	var err error
	if mbox == "" {
		if st, err = objectstore.Open(sess.mailboxPath); err != nil {
			http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
			return
		}
	} else {
		var addr string
		var sok bool
		if st, addr, sok = s.openSharedFor(sess, mbox); !sok {
			http.NotFound(w, r)
			return
		}
		mbox = addr
	}
	defer st.Close()

	op := r.FormValue("op")
	// Managing folders in a shared mailbox is authorized per the caller's folder
	// rights; the own mailbox needs no check.
	if mbox != "" && !sharedFolderAllowed(st, sess.user, op, r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch op {
	case "create":
		s.createFolder(w, r, st, mbox)
	case "rename":
		s.renameFolder(w, r, st, mbox)
	case "delete":
		s.deleteFolder(w, r, st, mbox)
	default:
		http.Error(w, "unknown folder action", http.StatusBadRequest)
	}
}

// validFolderName trims and validates a folder name: non-empty and free of the
// hierarchy separator (which would collide with path nesting in the sidebar).
func validFolderName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || strings.Contains(name, hierarchySep) {
		return "", false
	}
	return name, true
}

// createFolder creates a top-level user folder, refusing a blank/invalid name or
// a duplicate of an existing top-level folder.
func (s *Server) createFolder(w http.ResponseWriter, r *http.Request, st *objectstore.Store, mbox string) {
	name, ok := validFolderName(r.FormValue("name"))
	if !ok {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	if _, exists, err := st.FolderByName(nil, name); err != nil {
		http.Error(w, "cannot check folders", http.StatusInternalServerError)
		return
	} else if exists {
		http.Error(w, "a folder with that name already exists", http.StatusBadRequest)
		return
	}
	if _, err := st.CreateFolder(nil, name); err != nil {
		http.Error(w, "cannot create folder", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, mailboxRedirect(mbox), http.StatusSeeOther)
}

// renameFolder renames a user folder in place. The folder's CURRENT parent is
// passed so the rename does not silently reparent a nested folder to the top.
func (s *Server) renameFolder(w http.ResponseWriter, r *http.Request, st *objectstore.Store, mbox string) {
	id, ok := userFolderID(r.FormValue("id"))
	if !ok {
		http.Error(w, "cannot rename a built-in folder", http.StatusForbidden)
		return
	}
	name, ok := validFolderName(r.FormValue("name"))
	if !ok {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	parent, found := folderParent(folders, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	if err := st.RenameFolder(id, parent, name); err != nil {
		http.Error(w, "cannot rename folder", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, mailboxRedirect(mbox), http.StatusSeeOther)
}

// deleteFolder permanently deletes a user folder and everything under it (a
// cascade — the only primitive the store offers; the UI confirms first).
func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request, st *objectstore.Store, mbox string) {
	id, ok := userFolderID(r.FormValue("id"))
	if !ok {
		http.Error(w, "cannot delete a built-in folder", http.StatusForbidden)
		return
	}
	if err := st.DeleteFolder(id); err != nil {
		http.Error(w, "cannot delete folder", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, mailboxRedirect(mbox), http.StatusSeeOther)
}

// userFolderID parses a folder id and reports it only when it is a user-created
// folder (id >= the unassigned-start id); built-in folders are rejected.
func userFolderID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < int64(mapi.PrivateFIDUnassignedStart) {
		return 0, false
	}
	return id, true
}
