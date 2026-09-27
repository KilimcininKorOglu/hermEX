package admin

import (
	"errors"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// quarantineMsg is one Junk-folder message shown in the admin quarantine review:
// metadata only (date, sender, subject, size), never the body, so an admin
// reviewing a user's quarantine for false positives does not read their mail.
type quarantineMsg struct {
	UID     uint32
	Date    stamp
	Sender  string
	Subject string
	Size    string
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
	s.renderQuarantine(w, r, r.PathValue("email"), maildir, csrfCookieValue(r), panelNotice{})
}

// handleUIQuarantineRelease moves a quarantined message from Junk back to the inbox,
// the admin has judged it a false positive (ham), so it is filed without re-scoring,
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
	notice := okNotice("userQuarantine.released")
	if err := s.quarantineMutate(maildir, func(st *objectstore.Store) error {
		_, err := st.MoveMessage(int64(mapi.PrivateFIDJunk), uid, int64(mapi.PrivateFIDInbox))
		return err
	}); err != nil {
		notice = warnNotice("userQuarantine.releaseFailed")
	}
	s.renderQuarantine(w, r, r.PathValue("email"), maildir, csrfCookieValue(r), notice)
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
	notice := okNotice("userQuarantine.deleted")
	if err := s.quarantineMutate(maildir, func(st *objectstore.Store) error {
		return st.DeleteMessage(int64(mapi.PrivateFIDJunk), uid)
	}); err != nil {
		notice = warnNotice("userQuarantine.deleteFailed")
	}
	s.renderQuarantine(w, r, r.PathValue("email"), maildir, csrfCookieValue(r), notice)
}

// quarantineUID reads the target message UID from the posted form.
func quarantineUID(r *http.Request) uint32 {
	uid, _ := strconv.ParseUint(r.PostFormValue("uid"), 10, 32)
	return uint32(uid)
}

// quarantineMutate runs fn against the existing mailbox store. A mailbox that was
// never provisioned has no quarantined message to act on, so it is not created.
func (s *Server) quarantineMutate(maildir string, fn func(*objectstore.Store) error) error {
	return withExistingStore(maildir, fn)
}

// renderQuarantine reads the user's Junk folder and renders the quarantine panel. A
// mailbox that was never provisioned shows an empty quarantine, and is not created
// by the read; a store that fails to open is reported.
func (s *Server) renderQuarantine(w http.ResponseWriter, r *http.Request, email, maildir, csrf string, notice panelNotice) {
	data := map[string]any{"Email": email, "CSRF": csrf, "Notice": notice}
	st, err := objectstore.OpenExisting(maildir)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		s.render(w, r, "quarantine", data)
		return
	}
	if err != nil {
		data["Error"] = s.notice("userQuarantine.junkUnread", err)
		s.render(w, r, "quarantine", data)
		return
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	if err != nil {
		data["Error"] = s.notice("userQuarantine.junkUnread", err)
		s.render(w, r, "quarantine", data)
		return
	}
	c, lang := requestClock(r), requestLang(r)
	views := make([]quarantineMsg, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, quarantineMsg{
			UID:     m.UID,
			Date:    c.at(m.InternalDate),
			Sender:  m.Sender,
			Subject: m.Subject,
			Size:    sizeMsg(m.Size, lang),
		})
	}
	data["Messages"] = views
	s.render(w, r, "quarantine", data)
}
