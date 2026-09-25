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
	s.editOccurrence(w, r, occurrenceChange{at: at, method: "REQUEST", edit: func(raw []byte) ([]byte, bool) {
		return oxcical.MoveOccurrence(raw, at, start, end)
	}})
}

// handleDeleteOccurrence removes one instance of a series, named by the instant
// in the at query parameter; the other instances stay.
func (s *Server) handleDeleteOccurrence(w http.ResponseWriter, r *http.Request) {
	at, err := time.Parse(time.RFC3339, r.URL.Query().Get("at"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.editOccurrence(w, r, occurrenceChange{at: at, method: "CANCEL", edit: func(raw []byte) ([]byte, bool) {
		if !oxcical.HasInstance(raw, at) {
			return nil, false
		}
		return oxcical.CancelOccurrence(raw, at)
	}})
}

// occurrenceChange is one instance edit: the instance's generated instant, the
// edit to the stored series, and the iTIP method that tells the attendees about
// it. An empty method tells nobody.
type occurrenceChange struct {
	at     time.Time
	method string
	edit   func([]byte) ([]byte, bool)
}

// editOccurrence applies one instance edit to the series the path names and
// writes the result onto the same stored appointment, then tells the attendees
// when the caller organizes the meeting. edit reports false when the series has
// no such instance, which answers 404.
func (s *Server) editOccurrence(w http.ResponseWriter, r *http.Request, ch occurrenceChange) {
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, c, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	notice, err := editSeries(st, id, c.Email, ch)
	if errors.Is(err, errNoSuchEvent) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such occurrence"})
		return
	}
	if err != nil {
		logError("calendar-occurrence", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	notice.send(s, st)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// editSeries rewrites a stored series from its iCalendar after the change edited
// it. When caller organizes the meeting, the revision advances in the stored
// series and the message that tells the attendees is returned, prepared from the
// object in memory and sent by the caller only once the write succeeded.
func editSeries(st *objectstore.Store, id int64, caller string, ch occurrenceChange) (*pendingMail, error) {
	stored, err := st.OpenMessage(id)
	if errors.Is(err, objectstore.ErrNotFound) || (err == nil && !isAppointment(stored.Props)) {
		return nil, errNoSuchEvent
	}
	if err != nil {
		return nil, err
	}
	v, _ := stored.Props.Get(mapi.PrIcalOriginal)
	raw, _ := v.([]byte)
	edited, ok := ch.edit(raw)
	if !ok {
		return nil, errNoSuchEvent
	}
	var notice *pendingMail
	if ch.method != "" && isOrganizer(st, stored.Props, caller) {
		edited, notice = ch.announce(st, id, stored.Props, raw, edited, caller)
	}
	return notice, rewriteEvent(st, id, edited, oxcical.Options{Resolver: st.GetNamedPropIDs})
}

// announce advances the revision in the edited object and prepares the
// single-instance message for the attendees. A cancellation advances the series
// revision and describes the instance as it stood before the edit removed it; an
// update advances the moved instance's own revision past the series' and
// describes the instance as edited. It returns the edited object unchanged and no
// message when there is nobody to tell or nothing to render.
func (ch occurrenceChange) announce(st *objectstore.Store, id int64, props mapi.PropertyValues, before, edited []byte, organizer string) ([]byte, *pendingMail) {
	to := meetingRecipients(st, id, organizer)
	revised, seq, ok := ch.revise(edited)
	if len(to) == 0 || !ok {
		return edited, nil
	}
	source := before
	if ch.method != "CANCEL" {
		source = revised
	}
	body, ok := oxcical.InstanceBody(source, ch.at, ch.method, seq)
	if !ok {
		return edited, nil
	}
	if isLegacyMeeting(st, id, props) {
		if withUID, ok := oxcical.WithUID(body, strconv.FormatInt(id, 10)); ok {
			body = withUID
		}
	}
	return revised, &pendingMail{organizer: organizer, to: to, mail: ch.mail(propStr(props, mapi.PrSubject), body)}
}

// revise writes the next revision into the edited object: the series' for a
// cancellation, the instance's own for an update, which must pass the series'
// because the instance started from the series master.
func (ch occurrenceChange) revise(edited []byte) ([]byte, int, bool) {
	if ch.method == "CANCEL" {
		seq := oxcical.Sequence(edited, nil) + 1
		revised, ok := oxcical.SetSequence(edited, seq, nil)
		return revised, seq, ok
	}
	seq := max(oxcical.Sequence(edited, nil), oxcical.Sequence(edited, &ch.at)) + 1
	revised, ok := oxcical.SetSequence(edited, seq, &ch.at)
	return revised, seq, ok
}

// mail is the scheduling message the change sends, under the meeting's subject.
func (ch occurrenceChange) mail(subject string, body []byte) meetingMail {
	if ch.method == "CANCEL" {
		subject = "Canceled: " + subject
		return meetingMail{method: ch.method, subject: subject, text: subject, kind: "meeting-occurrence-cancellation", calendar: body}
	}
	return meetingMail{method: ch.method, subject: subject, text: subject, kind: "meeting-occurrence-update", calendar: body}
}
