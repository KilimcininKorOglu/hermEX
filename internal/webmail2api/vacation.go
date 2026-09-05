package webmail2api

import (
	"encoding/json"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// vacationJSON is the SPA's VacationAutoReply shape.
type vacationJSON struct {
	Enabled         bool   `json:"enabled"`
	Subject         string `json:"subject"`
	Message         string `json:"message"`
	HTMLMessage     string `json:"html_message,omitempty"`
	ExternalMessage string `json:"external_message,omitempty"`
	Audience        string `json:"audience,omitempty"`
	StartDate       string `json:"start_date,omitempty"`
	EndDate         string `json:"end_date,omitempty"`
	// SubjectPrefix is READ-ONLY: the operator's fallback wording, served so the
	// form can show what an empty subject will produce. A value the client sends
	// back is ignored, because this is not the user's setting to change.
	SubjectPrefix string `json:"subject_prefix,omitempty"`
}

func oofToVacation(o objectstore.OOFSettings) vacationJSON {
	v := vacationJSON{
		Enabled:         o.Enabled,
		Subject:         o.InternalSubject,
		Message:         o.InternalReply,
		ExternalMessage: o.ExternalReply,
		Audience:        "all",
	}
	if !o.ExternalEnabled {
		v.Audience = "internal"
	} else if o.ExternalAudience == objectstore.OOFExternalKnown {
		v.Audience = "external"
	}
	if o.Start > 0 {
		v.StartDate = time.Unix(o.Start, 0).UTC().Format(time.RFC3339)
	}
	if o.End > 0 {
		v.EndDate = time.Unix(o.End, 0).UTC().Format(time.RFC3339)
	}
	return v
}

func vacationToOOF(v vacationJSON) objectstore.OOFSettings {
	o := objectstore.OOFSettings{
		Enabled:         v.Enabled,
		InternalSubject: v.Subject,
		InternalReply:   v.Message,
		ExternalSubject: v.Subject,
		ExternalReply:   v.ExternalMessage,
	}
	if o.ExternalReply == "" {
		o.ExternalReply = v.Message
	}
	switch v.Audience {
	case "internal":
		o.ExternalEnabled = false
	case "external":
		o.ExternalEnabled, o.ExternalAudience = true, objectstore.OOFExternalKnown
	default:
		o.ExternalEnabled, o.ExternalAudience = true, objectstore.OOFExternalAll
	}
	if v.StartDate != "" {
		if t, err := time.Parse(time.RFC3339, v.StartDate); err == nil {
			o.Start = t.Unix()
		}
	}
	if v.EndDate != "" {
		if t, err := time.Parse(time.RFC3339, v.EndDate); err == nil {
			o.End = t.Unix()
		}
	}
	return o
}

func (s *Server) handleGetVacation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	o, _ := st.GetOOFSettings()
	v := oofToVacation(o)
	v.SubjectPrefix = s.autoReplyPrefix()
	writeJSON(w, http.StatusOK, v)
}

// autoReplyPrefix returns the operator's out-of-office subject prefix, or the
// built-in default when the directory cannot report one. It is read per request
// rather than cached, because the value is a single indexed row and the endpoint
// is opened once per visit to the settings page.
func (s *Server) autoReplyPrefix() string {
	rd, ok := s.auth.(interface {
		GetAutoReplySettings() (directory.AutoReplySettings, bool, error)
	})
	if !ok {
		return directory.DefaultAutoReplySubjectPrefix
	}
	cfg, found, err := rd.GetAutoReplySettings()
	if err != nil || !found || cfg.SubjectPrefix == "" {
		return directory.DefaultAutoReplySubjectPrefix
	}
	return cfg.SubjectPrefix
}

func (s *Server) handlePutVacation(w http.ResponseWriter, r *http.Request) {
	var v vacationJSON
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	if err := st.SetOOFSettings(vacationToOOF(v)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
		return
	}
	// The prefix is the operator's, not the user's: whatever the client sent is
	// replaced by the stored one, so the echo cannot report a value nobody saved.
	v.SubjectPrefix = s.autoReplyPrefix()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteVacation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	_ = st.SetOOFSettings(objectstore.OOFSettings{})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
