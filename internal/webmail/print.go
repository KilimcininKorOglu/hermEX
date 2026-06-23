package webmail

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// importanceLabel maps a message's PR_IMPORTANCE to a print label, returning ""
// for Normal or absent (only High/Low are worth a header row).
func importanceLabel(st *objectstore.Store, messageID int64) string {
	props, err := st.GetMessageProperties(messageID, mapi.PrImportance)
	if err != nil {
		return ""
	}
	val, ok := props.Get(mapi.PrImportance)
	if !ok {
		return ""
	}
	n, ok := val.(int32)
	if !ok {
		return ""
	}
	switch int(n) {
	case mapi.ImportanceHigh:
		return "High"
	case mapi.ImportanceLow:
		return "Low"
	}
	return ""
}

// sensitivityLabel maps a message's PR_SENSITIVITY to a print label, returning ""
// for Normal or absent.
func sensitivityLabel(st *objectstore.Store, messageID int64) string {
	props, err := st.GetMessageProperties(messageID, mapi.PrSensitivity)
	if err != nil {
		return ""
	}
	val, ok := props.Get(mapi.PrSensitivity)
	if !ok {
		return ""
	}
	n, ok := val.(int32)
	if !ok {
		return ""
	}
	switch int(n) {
	case mapi.SensitivityPersonal:
		return "Personal"
	case mapi.SensitivityPrivate:
		return "Private"
	case mapi.SensitivityConfidential:
		return "Confidential"
	}
	return ""
}

// handlePrint renders a standalone, print-optimized view of one message: a
// formatted header block (From, Sent, To, Cc, Subject, Importance, Sensitivity,
// Attachments) above the body, which auto-invokes window.print() on load. It is
// read-only — printing does not change the message's \Seen state.
func (s *Server) handlePrint(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Shared-mailbox print is not wired here yet; reject an mbox-scoped request
	// rather than silently printing from the caller's own mailbox.
	if denyShared(w, r) {
		return
	}
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	uid := uint32(uid64)

	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	cfg, err := loadSettings(st)
	if err != nil {
		cfg = defaultSettings()
	}
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
	raw, err := st.GetMessageRaw(folderID, uid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Print honors the same plain-text display preference as the reader, so the
	// printout matches what the user sees on screen.
	detail := buildMessageDetail(raw, folder, uid, cfg.IncomingRender == "plain", cfg.SafeSenders)
	if m, err := st.MessageByUID(folderID, uid); err == nil {
		detail.Importance = importanceLabel(st, m.ID)
		detail.Sensitivity = sensitivityLabel(st, m.ID)
	}
	s.render(w, "print", detail)
}
