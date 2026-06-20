package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// handleGetUserOOF returns a user's out-of-office settings (system administrators
// only). The settings live in the mailbox's object store, addressed by the user's
// maildir resolved from the directory.
func (s *Server) handleGetUserOOF(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	cfg, err := s.store.GetOOFSettings(u.Maildir)
	if err != nil {
		http.Error(w, "could not read out-of-office settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, cfg)
}

// handleSetUserOOF replaces a user's out-of-office settings (system administrators
// only). The whole settings object is replaced; the JSON body is the canonical
// objectstore representation, so the admin shares one encoding with webmail and
// the delivery path.
func (s *Server) handleSetUserOOF(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	var cfg objectstore.OOFSettings
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetOOFSettings(u.Maildir, cfg); err != nil {
		http.Error(w, "could not save out-of-office settings", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// oofView is the out-of-office section's template model: the stored settings with
// the schedule bounds formatted as datetime-local field values and the known-only
// external audience reduced to a checkbox.
type oofView struct {
	Enabled         bool
	InternalSubject string
	InternalReply   string
	ExternalSubject string
	ExternalReply   string
	ExternalEnabled bool
	KnownOnly       bool
	Start           string
	End             string
}

// oofTimeLayout is the wire form of an HTML datetime-local field: wall-clock with
// no timezone, read in the server's local zone (matching the webmail OOF form).
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

// oofViewOf builds the template model from stored settings.
func oofViewOf(cfg objectstore.OOFSettings) oofView {
	return oofView{
		Enabled:         cfg.Enabled,
		InternalSubject: cfg.InternalSubject,
		InternalReply:   cfg.InternalReply,
		ExternalSubject: cfg.ExternalSubject,
		ExternalReply:   cfg.ExternalReply,
		ExternalEnabled: cfg.ExternalEnabled,
		KnownOnly:       cfg.ExternalAudience == objectstore.OOFExternalKnown,
		Start:           formatOOFTime(cfg.Start),
		End:             formatOOFTime(cfg.End),
	}
}

// oofFromForm reads the out-of-office form into the canonical settings, mapping
// the known-only checkbox onto the external audience.
func oofFromForm(r *http.Request) objectstore.OOFSettings {
	audience := objectstore.OOFExternalAll
	if r.PostFormValue("externalknownonly") != "" {
		audience = objectstore.OOFExternalKnown
	}
	return objectstore.OOFSettings{
		Enabled:          r.PostFormValue("enabled") != "",
		InternalSubject:  strings.TrimSpace(r.PostFormValue("internalsubject")),
		InternalReply:    r.PostFormValue("internalreply"),
		ExternalSubject:  strings.TrimSpace(r.PostFormValue("externalsubject")),
		ExternalReply:    r.PostFormValue("externalreply"),
		ExternalEnabled:  r.PostFormValue("externalenabled") != "",
		ExternalAudience: audience,
		Start:            parseOOFTime(r.PostFormValue("start")),
		End:              parseOOFTime(r.PostFormValue("end")),
	}
}

// handleUIUserOOF saves the user's out-of-office settings from the detail form and
// returns the refreshed status panel; a store error is reported in the panel
// rather than failing the request.
func (s *Server) handleUIUserOOF(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Server error."
	case !ok:
		data["Error"] = "No such user."
	default:
		if err := s.store.SetOOFSettings(u.Maildir, oofFromForm(r)); err != nil {
			data["Error"] = "Could not save out-of-office settings: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}
