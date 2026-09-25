package webmail2api

import (
	"net/http"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// meetingSettingsJSON is the part of the mailbox's meeting handling the user sets
// in webmail. Automatic acceptance stays with the operator, so it is neither shown
// nor written here. A PUT field left out keeps its stored value, because a
// setting that reads a missing field as false would turn cancellation processing
// off for a client that never knew about it.
type meetingSettingsJSON struct {
	RemoveRequestOnResponse *bool `json:"removeRequestOnResponse"`
	ProcessCancellations    *bool `json:"processCancellations"`
}

// handleGetMeetingSettings returns the caller's meeting handling settings.
func (s *Server) handleGetMeetingSettings(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	cfg, err := st.GetMeetingConfig()
	if err != nil {
		logError("meeting-settings", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settings unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, meetingSettingsOf(cfg))
}

// handlePutMeetingSettings writes the caller's meeting handling settings, keeping
// the operator's automatic-processing settings as they are.
func (s *Server) handlePutMeetingSettings(w http.ResponseWriter, r *http.Request) {
	var in meetingSettingsJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, c, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	// The configuration is written whole, so the read that precedes the write is
	// serialized with every other settings write to this mailbox.
	unlock := s.lockSettings(c.Mailbox)
	defer unlock()
	cfg, err := st.GetMeetingConfig()
	if err == nil {
		cfg = in.applyTo(cfg)
		err = st.SetMeetingConfig(cfg)
	}
	if err != nil {
		logError("meeting-settings", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save settings"})
		return
	}
	writeJSON(w, http.StatusOK, meetingSettingsOf(cfg))
}

// meetingSettingsOf is the webmail view of a stored configuration.
func meetingSettingsOf(cfg objectstore.MeetingConfig) meetingSettingsJSON {
	remove, process := cfg.RemoveRequestOnResponse, !cfg.LeaveCancellationsUnprocessed
	return meetingSettingsJSON{RemoveRequestOnResponse: &remove, ProcessCancellations: &process}
}

// applyTo writes the fields the request names onto a stored configuration.
func (in meetingSettingsJSON) applyTo(cfg objectstore.MeetingConfig) objectstore.MeetingConfig {
	if in.RemoveRequestOnResponse != nil {
		cfg.RemoveRequestOnResponse = *in.RemoveRequestOnResponse
	}
	if in.ProcessCancellations != nil {
		cfg.LeaveCancellationsUnprocessed = !*in.ProcessCancellations
	}
	return cfg
}
