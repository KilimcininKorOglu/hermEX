package webmail

import (
	"net/http"
	"net/url"
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
	case "unschedule":
		s.unscheduleSend(w, st, folderID, uid)
	case "move":
		s.moveTo(w, r, st, folders, folderID, folder, uid)
	case "copy":
		s.copyTo(w, r, st, folders, folderID, uid)
	case "junk":
		s.junkMessage(w, st, folderID, uid)
	case "restore":
		s.restoreMessage(w, st, folderID, uid)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// copyMessage re-files a message into dst, preserving its flags and internal
// date (a fresh uid is assigned in dst, and the wire form is re-synthesized — the
// same copy-then-delete primitive the delete-to-Trash path uses).
func copyMessage(st *objectstore.Store, src int64, uid uint32, dst int64) error {
	m, err := st.MessageByUID(src, uid)
	if err != nil {
		return err
	}
	raw, err := st.GetMessageRaw(src, uid)
	if err != nil {
		return err
	}
	_, err = st.AppendMessage(dst, raw, m.InternalDate, m.Flags)
	return err
}

// moveMessage copies a message into dst then removes the source copy. Not
// transactional: a crash between the two leaves a duplicate, never data loss
// (matching the existing delete-to-Trash behavior).
func moveMessage(st *objectstore.Store, src int64, uid uint32, dst int64) error {
	if err := copyMessage(st, src, uid, dst); err != nil {
		return err
	}
	return st.DeleteMessage(src, uid)
}

// folderExists reports whether id is one of the mailbox's visible folders.
func folderExists(folders []objectstore.FolderInfo, id int64) bool {
	for _, f := range folders {
		if f.ID == id {
			return true
		}
	}
	return false
}

// parseDst reads the "dst" form value (a folder id) and validates it is a known,
// mail-typed folder (a valid move/copy target).
func parseDst(r *http.Request, folders []objectstore.FolderInfo) (int64, bool) {
	dst, err := strconv.ParseInt(r.FormValue("dst"), 10, 64)
	if err != nil || !folderExists(folders, dst) || !isMailFolder(dst) {
		return 0, false
	}
	return dst, true
}

// moveTo moves a message to the folder named by the "dst" form value (a folder
// id). A move onto the same folder is a no-op. On success it asks htmx to
// navigate back to the source folder, since the message has left the open reader.
func (s *Server) moveTo(w http.ResponseWriter, r *http.Request, st *objectstore.Store, folders []objectstore.FolderInfo, src int64, folder string, uid uint32) {
	dst, ok := parseDst(r, folders)
	if !ok {
		http.Error(w, "invalid destination folder", http.StatusBadRequest)
		return
	}
	if dst == src {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := moveMessage(st, src, uid, dst); err != nil {
		http.Error(w, "cannot move message", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/mail?folder="+url.QueryEscape(folder))
	w.WriteHeader(http.StatusOK)
}

// copyTo copies a message to the "dst" folder, leaving the source in place (so
// the reader stays valid; no redirect). A copy onto the same folder is a no-op.
func (s *Server) copyTo(w http.ResponseWriter, r *http.Request, st *objectstore.Store, folders []objectstore.FolderInfo, src int64, uid uint32) {
	dst, ok := parseDst(r, folders)
	if !ok {
		http.Error(w, "invalid destination folder", http.StatusBadRequest)
		return
	}
	if dst == src {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := copyMessage(st, src, uid, dst); err != nil {
		http.Error(w, "cannot copy message", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// junkMessage moves a message to the Junk Email folder (a no-op when it is
// already there). The response is empty so htmx removes the row.
func (s *Server) junkMessage(w http.ResponseWriter, st *objectstore.Store, folderID int64, uid uint32) {
	junkID := int64(mapi.PrivateFIDJunk)
	if folderID == junkID {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := moveMessage(st, folderID, uid, junkID); err != nil {
		http.Error(w, "cannot move to Junk", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// restoreMessage moves a message out of Deleted Items or Junk back to the Inbox.
// It is valid only from those two folders (the row offers Restore only there);
// from anywhere else it is rejected. The response is empty so htmx removes the row.
func (s *Server) restoreMessage(w http.ResponseWriter, st *objectstore.Store, folderID int64, uid uint32) {
	if folderID != int64(mapi.PrivateFIDDeletedItems) && folderID != int64(mapi.PrivateFIDJunk) {
		http.Error(w, "restore is only valid from Deleted Items or Junk", http.StatusBadRequest)
		return
	}
	if err := moveMessage(st, folderID, uid, int64(mapi.PrivateFIDInbox)); err != nil {
		http.Error(w, "cannot restore message", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// unscheduleSend cancels a scheduled send by moving the Outbox message back to
// Drafts. The re-synthesized wire form carries no deferred-send property (it is
// not a header), so re-filing it produces a plain editable draft without the
// schedule — the clearest way to drop the deferral. The response is empty so htmx
// removes the row.
func (s *Server) unscheduleSend(w http.ResponseWriter, st *objectstore.Store, folderID int64, uid uint32) {
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
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDDraft), raw, m.InternalDate, objectstore.FlagSeen|objectstore.FlagDraft); err != nil {
		http.Error(w, "cannot move to Drafts", http.StatusInternalServerError)
		return
	}
	st.DeleteMessage(folderID, uid)
	w.WriteHeader(http.StatusOK)
}

// toggleFlag flips a single flag bit and re-renders the message row. The row is
// enriched with its per-row icon columns (attachment paperclip, importance
// marker) the same way the list pipeline enriches each visible page row; without
// this the single-row htmx swap would drop those icons until a full reload, since
// they come from a per-message object read rather than the index row.
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
	v := messageViewFrom(folderID, folder, m)
	enrichIcons(st, m.ID, &v)
	s.render(w, "messagerow", v)
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
