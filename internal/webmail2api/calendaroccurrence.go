package webmail2api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// occurrenceMoveJSON is the body of an occurrence move: the instance's generated
// instant, as a listing reported it, and its new span.
type occurrenceMoveJSON struct {
	Occurrence string `json:"occurrence"`
	Start      string `json:"start"`
	End        string `json:"end"`
}

// handleMoveOccurrence moves one instance of a series to a new span, leaving the
// other instances where they are.
func (s *Server) handleMoveOccurrence(w http.ResponseWriter, r *http.Request) {
	var in occurrenceMoveJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	at, err1 := time.Parse(time.RFC3339, in.Occurrence)
	start, err2 := time.Parse(time.RFC3339, in.Start)
	end, err3 := time.Parse(time.RFC3339, in.End)
	if err := errors.Join(err1, err2, err3); err != nil || end.Before(start) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.editOccurrence(w, r, func(raw []byte) ([]byte, bool) {
		return oxcical.MoveOccurrence(raw, at, start, end)
	})
}

// handleDeleteOccurrence removes one instance of a series, named by the instant
// in the at query parameter; the other instances stay.
func (s *Server) handleDeleteOccurrence(w http.ResponseWriter, r *http.Request) {
	at, err := time.Parse(time.RFC3339, r.URL.Query().Get("at"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.editOccurrence(w, r, func(raw []byte) ([]byte, bool) {
		if !oxcical.HasInstance(raw, at) {
			return nil, false
		}
		return oxcical.CancelOccurrence(raw, at)
	})
}

// editOccurrence applies one instance edit to the series the path names and
// writes the result onto the same stored appointment. edit reports false when
// the series has no such instance, which answers 404.
func (s *Server) editOccurrence(w http.ResponseWriter, r *http.Request, edit func([]byte) ([]byte, bool)) {
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	err = editSeries(st, id, edit)
	if errors.Is(err, errNoSuchEvent) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such occurrence"})
		return
	}
	if err != nil {
		logError("calendar-occurrence", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// editSeries rewrites a stored series from its iCalendar after edit changed it.
func editSeries(st *objectstore.Store, id int64, edit func([]byte) ([]byte, bool)) error {
	stored, err := st.OpenMessage(id)
	if errors.Is(err, objectstore.ErrNotFound) || (err == nil && !isAppointment(stored.Props)) {
		return errNoSuchEvent
	}
	if err != nil {
		return err
	}
	v, _ := stored.Props.Get(mapi.PrIcalOriginal)
	raw, _ := v.([]byte)
	edited, ok := edit(raw)
	if !ok {
		return errNoSuchEvent
	}
	return rewriteEvent(st, id, edited, oxcical.Options{Resolver: st.GetNamedPropIDs})
}
