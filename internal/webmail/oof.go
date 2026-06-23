package webmail

import (
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// oofPage is the view model for the out-of-office settings form. Start and End
// are rendered as datetime-local field values ("2006-01-02T15:04", empty when
// unset); the handler converts to and from the stored unix seconds.
// ExternalKnownOnly is the OOFExternalKnown audience rendered as a checkbox:
// checked replies only to known contacts, unchecked replies to every external
// sender.
type oofPage struct {
	Enabled           bool
	InternalSubject   string
	InternalReply     string
	ExternalSubject   string
	ExternalReply     string
	ExternalEnabled   bool
	ExternalKnownOnly bool
	Start             string
	End               string
	Saved             bool
}

// oofTimeLayout is the wire form of an HTML datetime-local field: wall-clock
// with no timezone, so it is interpreted in the server's local zone (matching
// the send-later scheduler).
const oofTimeLayout = "2006-01-02T15:04"

// formatOOFTime renders a stored unix time as a datetime-local value; the
// open-ended bound (0) renders empty.
func formatOOFTime(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).Local().Format(oofTimeLayout)
}

// parseOOFTime parses a datetime-local field value to unix seconds; an empty or
// unparseable value is the open-ended bound (0).
func parseOOFTime(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	t, err := time.ParseInLocation(oofTimeLayout, v, time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// handleOOFForm redirects the former standalone out-of-office page to its tab on
// the unified settings page; the POST endpoint below still serves the form.
func (s *Server) handleOOFForm(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings?tab=oof", http.StatusSeeOther)
}

// buildOOFPage loads the out-of-office section from the mailbox configuration.
// Best-effort — a read failure yields the empty (disabled) form rather than
// failing the whole settings page.
func buildOOFPage(st *objectstore.Store) oofPage {
	cfg, err := st.GetOOFSettings()
	if err != nil {
		return oofPage{}
	}
	return oofPage{
		Enabled:           cfg.Enabled,
		InternalSubject:   cfg.InternalSubject,
		InternalReply:     cfg.InternalReply,
		ExternalSubject:   cfg.ExternalSubject,
		ExternalReply:     cfg.ExternalReply,
		ExternalEnabled:   cfg.ExternalEnabled,
		ExternalKnownOnly: cfg.ExternalAudience == objectstore.OOFExternalKnown,
		Start:             formatOOFTime(cfg.Start),
		End:               formatOOFTime(cfg.End),
	}
}

// handleOOFSubmit stores the submitted out-of-office settings and redirects back
// to the form. Enabling out-of-office turns on the standard PR_OOF_STATE flag
// too (SetOOFSettings keeps them in sync), so the delivery path acts on it.
func (s *Server) handleOOFSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()

	audience := objectstore.OOFExternalAll
	if r.FormValue("externalknownonly") != "" {
		audience = objectstore.OOFExternalKnown
	}
	cfg := objectstore.OOFSettings{
		Enabled:          r.FormValue("enabled") != "",
		InternalSubject:  strings.TrimSpace(r.FormValue("internalsubject")),
		InternalReply:    r.FormValue("internalreply"),
		ExternalSubject:  strings.TrimSpace(r.FormValue("externalsubject")),
		ExternalReply:    r.FormValue("externalreply"),
		ExternalEnabled:  r.FormValue("externalenabled") != "",
		ExternalAudience: audience,
		Start:            parseOOFTime(r.FormValue("start")),
		End:              parseOOFTime(r.FormValue("end")),
	}
	if err := st.SetOOFSettings(cfg); err != nil {
		http.Error(w, "could not save out-of-office settings", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?tab=oof&saved=1", http.StatusSeeOther)
}
