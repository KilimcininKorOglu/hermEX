package webmail

import (
	"bytes"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"time"

	"hermex/internal/objectstore"
)

// importView is the data the import page renders: the destination folders to
// choose from, and an error from a rejected attempt.
type importView struct {
	Folders []folderView
	Error   string
}

// maxImportBytes bounds an uploaded .eml so a large file cannot exhaust memory.
const maxImportBytes = 50 << 20

// handleImportForm renders the import page: a folder picker and a file input for
// uploading an .eml into the mailbox.
func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "import", importView{Folders: s.folderViews(sess.mailboxPath)})
}

// handleImportSubmit stores an uploaded .eml into the chosen folder. The file is
// validated as a parseable RFC 5322 message before storage — oxcmail import is
// lenient and would otherwise file arbitrary bytes as an empty note.
func (s *Server) handleImportSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Importing into a shared mailbox is not wired here yet; reject an mbox-scoped
	// request rather than importing into the caller's own mailbox.
	if denyShared(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.importError(w, sess.mailboxPath, http.StatusBadRequest, "The upload was too large or malformed.")
		return
	}
	target := r.FormValue("folder")

	f, _, err := r.FormFile("eml")
	if err != nil {
		s.importError(w, sess.mailboxPath, http.StatusBadRequest, "Choose an .eml file to import.")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil || len(raw) == 0 {
		s.importError(w, sess.mailboxPath, http.StatusBadRequest, "The uploaded file was empty.")
		return
	}
	if _, err := mail.ReadMessage(bytes.NewReader(raw)); err != nil {
		s.importError(w, sess.mailboxPath, http.StatusBadRequest, "That file is not a valid email message.")
		return
	}

	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		s.importError(w, sess.mailboxPath, http.StatusInternalServerError, "Mailbox unavailable.")
		return
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		s.importError(w, sess.mailboxPath, http.StatusInternalServerError, "Could not read folders.")
		return
	}
	folderID, found := resolveFolder(folders, target)
	if !found {
		s.importError(w, sess.mailboxPath, http.StatusBadRequest, "Choose a destination folder.")
		return
	}
	if _, err := st.AppendMessage(folderID, raw, time.Now(), 0); err != nil {
		s.importError(w, sess.mailboxPath, http.StatusInternalServerError, "Could not import the message: "+err.Error())
		return
	}
	http.Redirect(w, r, "/mail?folder="+url.QueryEscape(target), http.StatusSeeOther)
}

// importError re-renders the import page with an error at the given status. It
// writes the status itself (s.render always implies 200), so the Content-Type is
// set before the status line.
func (s *Server) importError(w http.ResponseWriter, mailboxPath string, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.tmpl.ExecuteTemplate(w, "import", importView{Folders: s.folderViews(mailboxPath), Error: msg})
}
