package webmail

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handleAction applies a per-message action (toggle \Seen, toggle \Flagged, or
// delete) and returns the updated row partial, or an empty body for a delete
// (htmx removes the row).
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	folder := r.FormValue("folder")
	uid64, err := strconv.ParseUint(r.FormValue("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	uid := uint32(uid64)
	op := r.FormValue("op")

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
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}

	switch op {
	case "toggleseen":
		s.toggleFlag(w, st, folderID, folder, uid, objectstore.FlagSeen)
	case "toggleflag":
		s.toggleFlag(w, st, folderID, folder, uid, objectstore.FlagFlagged)
	case "delete":
		s.deleteMessage(w, st, folderID, uid)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// toggleFlag flips a single flag bit and re-renders the message row.
func (s *Server) toggleFlag(w http.ResponseWriter, st *objectstore.Store, folderID int64, folder string, uid uint32, bit int64) {
	cur, err := st.MessageFlags(folderID, uid)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if err := st.SetMessageFlags(folderID, uid, cur^bit); err != nil {
		http.Error(w, "cannot update flags", http.StatusInternalServerError)
		return
	}
	m, err := st.MessageByUID(folderID, uid)
	if err != nil {
		http.Error(w, "message gone", http.StatusInternalServerError)
		return
	}
	s.render(w, "messagerow", messageViewFrom(st, folderID, folder, m))
}

// deleteMessage moves a message to the Deleted Items folder, or removes it
// permanently when it is already there. The Deleted Items folder is a built-in
// addressed by its fixed id. The response is empty so htmx removes the row.
func (s *Server) deleteMessage(w http.ResponseWriter, st *objectstore.Store, folderID int64, uid uint32) {
	trashID := int64(mapi.PrivateFIDDeletedItems)
	if folderID == trashID {
		st.DeleteMessage(folderID, uid)
		w.WriteHeader(http.StatusOK)
		return
	}
	m, err := st.MessageByUID(folderID, uid)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	raw, err := st.GetMessageRaw(folderID, uid)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if _, err := st.AppendMessage(trashID, raw, m.InternalDate, m.Flags); err != nil {
		http.Error(w, "cannot move to Deleted Items", http.StatusInternalServerError)
		return
	}
	st.DeleteMessage(folderID, uid)
	w.WriteHeader(http.StatusOK)
}
