package webmail

import (
	"errors"
	"net/http"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"
)

// handleQuarantineReleaseForm shows the confirmation page for a digest release link. It
// deliberately does NOT release: link prefetchers — mail security scanners, link-rewrite
// proxies, client previews — issue GET requests, so releasing on GET would let the
// recipient's own security stack auto-unfilter the quarantined spam. Only the explicit
// POST below releases. The token is verified here too, so an invalid or expired link
// shows a clear message rather than a dead form.
func (s *Server) handleQuarantineReleaseForm(w http.ResponseWriter, r *http.Request) {
	if len(s.DigestSecret) == 0 {
		http.NotFound(w, r)
		return
	}
	tok := r.URL.Query().Get("t")
	if _, err := quarantine.Verify(s.DigestSecret, tok, time.Now()); err != nil {
		s.render(w, "quarantine", releaseError(err))
		return
	}
	s.render(w, "quarantine", map[string]any{"Confirm": true, "Token": tok})
}

// handleQuarantineRelease verifies the token and moves the one message it names from the
// Junk folder back to the inbox. The token is the sole credential — there is no session,
// so there is no ambient authority to forge: CSRF protection does not apply, and the
// confirm-page-then-POST exists to defeat link prefetch, not CSRF. Because Junk UIDs are
// never reused, a stale token can only ever find nothing, never the wrong message.
func (s *Server) handleQuarantineRelease(w http.ResponseWriter, r *http.Request) {
	if len(s.DigestSecret) == 0 {
		http.NotFound(w, r)
		return
	}
	claims, err := quarantine.Verify(s.DigestSecret, r.FormValue("t"), time.Now())
	if err != nil {
		s.render(w, "quarantine", releaseError(err))
		return
	}
	maildir, ok := s.accounts.Resolve(claims.Mailbox)
	if !ok {
		s.render(w, "quarantine", releaseResult("This mailbox could not be found.", "error"))
		return
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		s.render(w, "quarantine", releaseResult("Your mailbox is temporarily unavailable. Please try again later.", "error"))
		return
	}
	defer st.Close()
	if _, err := st.MoveMessage(int64(mapi.PrivateFIDJunk), claims.UID, int64(mapi.PrivateFIDInbox)); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			s.render(w, "quarantine", releaseResult("This message has already been handled — it is no longer in your Junk folder.", "notice"))
			return
		}
		s.releaseLog(claims.Mailbox, claims.UID, err.Error())
		s.render(w, "quarantine", releaseResult("This message could not be released right now. Please try again later.", "error"))
		return
	}
	s.releaseLog(claims.Mailbox, claims.UID, "")
	s.render(w, "quarantine", releaseResult("The message has been moved back to your inbox.", "notice"))
}

// releaseError maps a token-verification failure to a user-facing result, telling an
// expired link apart from an otherwise invalid one.
func releaseError(err error) map[string]any {
	if errors.Is(err, quarantine.ErrExpired) {
		return releaseResult("This release link has expired. Review your Junk folder in webmail instead.", "error")
	}
	return releaseResult("This release link is invalid.", "error")
}

// releaseResult builds the template data for a result (non-confirm) page; class is a
// CSS class ("notice" for success, "error" for failure).
func releaseResult(message, class string) map[string]any {
	return map[string]any{"Message": message, "Class": class}
}

// releaseLog records a quarantine-release outcome (errMsg empty on success) when a
// logger is configured.
func (s *Server) releaseLog(user string, uid uint32, errMsg string) {
	if s.Logger == nil {
		return
	}
	level := logging.LevelInfo
	if errMsg != "" {
		level = logging.LevelError
	}
	s.Logger.Emit(logging.Event{Level: level, Subsystem: logging.Webmail, Name: "quarantine.release", User: user, Err: errMsg, Fields: logging.Fields{"uid": uid}})
}
