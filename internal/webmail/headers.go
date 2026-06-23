package webmail

import (
	"bytes"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handleHeaders shows a message's internet (RFC 822) headers read-only, the
// reference's "Internet Headers" viewer. The headers are the SERVED message's:
// the store re-synthesizes the body on append, so not every original received
// trace header survives. html/template escapes the header text, so a crafted
// header value cannot inject markup into the page.
func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	mbox := mboxParam(r)
	var st *objectstore.Store
	if mbox == "" {
		if st, err = objectstore.Open(sess.mailboxPath); err != nil {
			http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
			return
		}
	} else {
		var sok bool
		if st, _, sok = s.openSharedFor(sess, mbox); !sok {
			http.NotFound(w, r)
			return
		}
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
	if mbox != "" {
		if rights, err := st.ResolvePermission(folderID, sess.user); err != nil || rights&mapi.FrightsReadAny == 0 {
			http.NotFound(w, r)
			return
		}
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subject := ""
	if m, err := st.MessageByUID(folderID, uint32(uid64)); err == nil {
		subject = m.Subject
	}
	s.render(w, "headers", map[string]any{
		"Headers": string(rawHeaders(raw)),
		"Subject": subject,
		"Folder":  folder,
		"UID":     uint32(uid64),
		"Mbox":    mbox,
	})
}

// rawHeaders returns the header block of a raw RFC 822 message: everything before
// the blank line that separates the headers from the body.
func rawHeaders(raw []byte) []byte {
	if head, _, ok := bytes.Cut(raw, []byte("\r\n\r\n")); ok {
		return head
	}
	if head, _, ok := bytes.Cut(raw, []byte("\n\n")); ok {
		return head
	}
	return raw
}
