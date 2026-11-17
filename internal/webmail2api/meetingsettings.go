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
	// Only the flags the request names are written, so the operator's automatic
	// acceptance, which the admin panel writes from another process, is kept.
	var leave *bool
	if in.ProcessCancellations != nil {
		v := !*in.ProcessCancellations
		leave = &v
	}
	err := st.SetMeetingResponseFlags(in.RemoveRequestOnResponse, leave)
	var cfg objectstore.MeetingConfig
	if err == nil {
		cfg, err = st.GetMeetingConfig()
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
