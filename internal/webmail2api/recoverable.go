package webmail2api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// handleRecoverableList returns a folder's Recoverable Items dumpster: the messages
// soft-deleted from it that are still recoverable. The folder defaults to Trash.
// owner targets a shared mailbox and is read-gated on the folder.
func (s *Server) handleRecoverableList(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer closeStore(mb.st)
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "trash"
	}
	fid, ok := resolveFolder(mb.st, folder)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return
	}
	if !mb.readAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	items, err := mb.st.ListSoftDeleted(fid)
	if err != nil {
		logError("recoverable-list", err, logging.Fields{"folder": fid})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot read dumpster"})
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"id":        strconv.FormatInt(it.MessageID, 10),
			"subject":   it.Subject,
			"from":      it.Sender,
			"date":      it.Date.UTC().Format(time.RFC3339),
			"deletedOn": it.DeletedOn.UTC().Format(time.RFC3339),
			"size":      it.Size,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": folder, "items": out})
}

// recoverableReq is the body for a recover or purge: the dumpster folder and the
// soft-deleted message's object id (as returned by handleRecoverableList).
type recoverableReq struct {
	Folder string `json:"folder"`
	ID     string `json:"id"`
}

// handleRecoverableRecover restores a soft-deleted message from the dumpster back
// into its folder. owner targets a shared mailbox and is write-gated on the folder.
func (s *Server) handleRecoverableRecover(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer closeStore(mb.st)
	fid, mid, ok := s.recoverableTarget(w, r, mb)
	if !ok {
		return
	}
	if _, err := mb.st.RecoverMessage(fid, mid); err != nil {
		answerDumpsterFailure(w, "recoverable-recover", err, fid, mid)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRecoverablePurge permanently removes a soft-deleted message from the
// dumpster. owner targets a shared mailbox and is write-gated on the folder.
func (s *Server) handleRecoverablePurge(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer closeStore(mb.st)
	fid, mid, ok := s.recoverableTarget(w, r, mb)
	if !ok {
		return
	}
	if err := mb.st.PurgeSoftDeletedInFolder(fid, mid); err != nil {
		answerDumpsterFailure(w, "recoverable-purge", err, fid, mid)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// answerDumpsterFailure answers 404 for an item that is not in the dumpster and
// records anything else before answering 500, so a store fault is not reported
// to the user as an item that is gone.
func answerDumpsterFailure(w http.ResponseWriter, op string, err error, fid, mid int64) {
	if errors.Is(err, objectstore.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	logError(op, err, logging.Fields{"folder": fid, "id": mid})
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the dumpster could not be changed"})
}

// recoverableTarget decodes and authorizes a recover/purge body: it resolves the
// folder, write-gates it (shared mailboxes), and parses the message id. It writes
// the error response and returns false on any failure.
func (s *Server) recoverableTarget(w http.ResponseWriter, r *http.Request, mb *mailboxCtx) (int64, int64, bool) {
	var req recoverableReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return 0, 0, false
	}
	fid, ok := resolveFolder(mb.st, req.Folder)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "folder not found"})
		return 0, 0, false
	}
	if !mb.writeAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return 0, 0, false
	}
	mid, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return 0, 0, false
	}
	return fid, mid, true
}
