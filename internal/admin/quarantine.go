package admin

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// quarantineMsg is one Junk-folder message shown in the admin quarantine review:
// metadata only (date, sender, subject, size), never the body — so an admin
// reviewing a user's quarantine for false positives does not read their mail.
type quarantineMsg struct {
	UID     uint32
	Date    string
	Sender  string
	Subject string
	Size    int64
}

// handleUIQuarantine renders a user's Junk folder for admin review: each message's
// metadata with per-message release and delete controls. It is a read page, gated on
// system read authority only; the state-changing release and delete are gated
// separately with CSRF.
func (s *Server) handleUIQuarantine(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	s.renderQuarantine(w, r.PathValue("email"), maildir, csrfCookieValue(r), "")
}

// handleUIQuarantineRelease moves a quarantined message from Junk back to the inbox —
// the admin has judged it a false positive (ham), so it is filed without re-scoring —
// and re-renders the panel. A UID that has since moved (the user acted via webmail, or
// new mail shifted the folder) yields a benign notice, not an error.
func (s *Server) handleUIQuarantineRelease(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	uid := quarantineUID(r)
	notice := "Released to the inbox."
	if err := s.quarantineMutate(maildir, func(st *objectstore.Store) error {
		_, err := st.MoveMessage(int64(mapi.PrivateFIDJunk), uid, int64(mapi.PrivateFIDInbox))
		return err
	}); err != nil {
		notice = "Could not release the message — it may already have been moved."
	}
	s.renderQuarantine(w, r.PathValue("email"), maildir, csrfCookieValue(r), notice)
}

// handleUIQuarantineDelete permanently deletes a quarantined message and re-renders
// the panel. As with release, a UID that is already gone yields a benign notice.
func (s *Server) handleUIQuarantineDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	uid := quarantineUID(r)
	notice := "Deleted."
	if err := s.quarantineMutate(maildir, func(st *objectstore.Store) error {
		return st.DeleteMessage(int64(mapi.PrivateFIDJunk), uid)
	}); err != nil {
		notice = "Could not delete the message — it may already be gone."
	}
	s.renderQuarantine(w, r.PathValue("email"), maildir, csrfCookieValue(r), notice)
}

// quarantineUID reads the target message UID from the posted form.
func quarantineUID(r *http.Request) uint32 {
	uid, _ := strconv.ParseUint(r.PostFormValue("uid"), 10, 32)
	return uint32(uid)
}

// quarantineMutate opens the mailbox store, runs fn against it, and closes it.
func (s *Server) quarantineMutate(maildir string, fn func(*objectstore.Store) error) error {
	st, err := objectstore.Open(maildir)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

// renderQuarantine reads the user's Junk folder and renders the quarantine panel. A
// mailbox that cannot be opened (e.g. one that was never provisioned) shows an empty
// quarantine rather than a failure — a clean mailbox is not an error.
func (s *Server) renderQuarantine(w http.ResponseWriter, email, maildir, csrf, notice string) {
	data := map[string]any{"Email": email, "CSRF": csrf, "Notice": notice}
	st, err := objectstore.Open(maildir)
	if err != nil {
		s.render(w, "quarantine", data)
		return
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	if err != nil {
		data["Error"] = "Could not read the Junk folder: " + err.Error()
		s.render(w, "quarantine", data)
		return
	}
	views := make([]quarantineMsg, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, quarantineMsg{
			UID:     m.UID,
			Date:    m.InternalDate.Format("2006-01-02 15:04"),
			Sender:  m.Sender,
			Subject: m.Subject,
			Size:    m.Size,
		})
	}
	data["Messages"] = views
	s.render(w, "quarantine", data)
}
