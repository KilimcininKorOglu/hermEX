package webmail2api

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/quarantine"
)

// handleQuarantineForm is the GET landing for a spam-digest release link. It
// verifies the token and shows a confirmation page whose POST performs the
// release — the confirm-then-POST defeats email-client link prefetch (the token
// is the sole credential, so there is no CSRF surface). Unauthenticated.
func (s *Server) handleQuarantineForm(w http.ResponseWriter, r *http.Request) {
	if len(s.DigestSecret) == 0 {
		http.NotFound(w, r)
		return
	}
	tok := r.URL.Query().Get("t")
	if _, err := quarantine.Verify(s.DigestSecret, tok, time.Now()); err != nil {
		writeQuarantinePage(w, http.StatusBadRequest, "Link expired", "This release link is invalid or has expired.", "")
		return
	}
	writeQuarantinePage(w, http.StatusOK, "Release quarantined message",
		"This message was held as spam. Release it back to your inbox?", tok)
}

// handleQuarantineRelease verifies the token and moves the one message it names
// from Junk back to the Inbox. Unauthenticated — the token is the only credential;
// since Junk UIDs are never reused, a stale token can only ever find nothing.
func (s *Server) handleQuarantineRelease(w http.ResponseWriter, r *http.Request) {
	if len(s.DigestSecret) == 0 {
		http.NotFound(w, r)
		return
	}
	claims, err := quarantine.Verify(s.DigestSecret, r.FormValue("t"), time.Now())
	if err != nil {
		writeQuarantinePage(w, http.StatusBadRequest, "Link expired", "This release link is invalid or has expired.", "")
		return
	}
	maildir, ok := s.accounts.Resolve(claims.Mailbox)
	if !ok {
		writeQuarantinePage(w, http.StatusNotFound, "Not found", "This mailbox could not be found.", "")
		return
	}
	st, err := objectstore.Open(maildir)
	if err != nil {
		writeQuarantinePage(w, http.StatusServiceUnavailable, "Unavailable",
			"Your mailbox is temporarily unavailable. Please try again later.", "")
		return
	}
	defer st.Close()
	if _, err := st.MoveMessage(int64(mapi.PrivateFIDJunk), claims.UID, int64(mapi.PrivateFIDInbox)); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			writeQuarantinePage(w, http.StatusOK, "Already handled",
				"This message has already been handled — it is no longer in your Junk folder.", "")
			return
		}
		writeQuarantinePage(w, http.StatusInternalServerError, "Could not release",
			"This message could not be released right now. Please try again later.", "")
		return
	}
	writeQuarantinePage(w, http.StatusOK, "Released", "The message has been moved back to your inbox.", "")
}

// writeQuarantinePage renders a minimal standalone result page (no SPA — the user
// arrives from an email link with no session). A non-empty confirmToken renders
// the release form.
func writeQuarantinePage(w http.ResponseWriter, status int, heading, message, confirmToken string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	form := ""
	if confirmToken != "" {
		form = `<form method="post" action="/quarantine/release"><input type="hidden" name="t" value="` +
			html.EscapeString(confirmToken) + `"><button type="submit">Release to inbox</button></form>`
	}
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width,initial-scale=1"><title>hermEX</title>`+
		`<style>body{font-family:system-ui,sans-serif;background:#f4f4f5;margin:0;display:flex;`+
		`min-height:100vh;align-items:center;justify-content:center}.card{background:#fff;border-radius:12px;`+
		`box-shadow:0 1px 3px rgba(0,0,0,.1);padding:2rem;max-width:28rem;text-align:center}`+
		`h1{color:#4f46e5;margin:0 0 1rem;font-size:1.25rem}h2{margin:0 0 .5rem;font-size:1.1rem}p{color:#52525b}`+
		`button{margin-top:1rem;background:#4f46e5;color:#fff;border:0;border-radius:8px;padding:.6rem 1.2rem;`+
		`font-size:1rem;cursor:pointer}</style></head><body><div class="card"><h1>hermEX</h1>`+
		`<h2>%s</h2><p>%s</p>%s</div></body></html>`,
		html.EscapeString(heading), html.EscapeString(message), form)
}
