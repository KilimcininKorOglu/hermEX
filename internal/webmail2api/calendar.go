package webmail2api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// eventJSON is the SPA's CalendarEvent shape (subset honored).
type eventJSON struct {
	UID         string `json:"uid"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Start       string `json:"start"`
	End         string `json:"end,omitempty"`
	AllDay      bool   `json:"allDay,omitempty"`
}

// toICalTime converts an RFC3339 (or YYYY-MM-DD all-day) value to iCal form.
func toICalTime(v string, allDay bool) string {
	if allDay {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			return ";VALUE=DATE:" + t.Format("20060102")
		}
		return ";VALUE=DATE:" + strings.ReplaceAll(v, "-", "")
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return ":" + t.UTC().Format("20060102T150405Z")
	}
	return ":" + v
}

// fromICalTime converts an iCal DTSTART/DTEND value back to RFC3339 / date.
func fromICalTime(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		return t.UTC().Format(time.RFC3339), false
	}
	if t, err := time.Parse("20060102T150405", v); err == nil {
		return t.Format(time.RFC3339), false
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t.Format("2006-01-02"), true
	}
	return v, false
}

// buildICal renders a minimal VEVENT for the proven oxcical import path.
func buildICal(e eventJSON) []byte {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//webmail2//EN\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", e.UID)
	fmt.Fprintf(&b, "SUMMARY:%s\r\n", e.Summary)
	fmt.Fprintf(&b, "DTSTART%s\r\n", toICalTime(e.Start, e.AllDay))
	if e.End != "" {
		fmt.Fprintf(&b, "DTEND%s\r\n", toICalTime(e.End, e.AllDay))
	}
	if e.Description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", e.Description)
	}
	if e.Location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\r\n", e.Location)
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return []byte(b.String())
}

// icalProp returns a property's value and the part of the key after its name
// (the parameters), ignoring folding.
func icalProp(ics []byte, name string) (value, params string) {
	for line := range strings.SplitSeq(string(ics), "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		base := key
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			base, params = key[:semi], key[semi:]
		}
		if strings.EqualFold(base, name) {
			return val, params
		}
	}
	return "", ""
}

func icalToEvent(ics []byte, id int64) eventJSON {
	e := eventJSON{UID: strconv.FormatInt(id, 10)}
	if v, _ := icalProp(ics, "UID"); v != "" {
		e.UID = v
	}
	e.Summary, _ = icalProp(ics, "SUMMARY")
	e.Description, _ = icalProp(ics, "DESCRIPTION")
	e.Location, _ = icalProp(ics, "LOCATION")
	if v, p := icalProp(ics, "DTSTART"); v != "" {
		e.Start, e.AllDay = fromICalTime(v)
		_ = p
	}
	if v, _ := icalProp(ics, "DTEND"); v != "" {
		e.End, _ = fromICalTime(v)
	}
	return e
}

func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	objs, err := st.ListFolderObjects(mapi.PrivateFIDCalendar)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []eventJSON{}})
		return
	}
	opt := oxcical.Options{Resolver: st.GetNamedPropIDs}
	events := make([]eventJSON, 0, len(objs))
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		ics, err := oxcical.Export(msg, opt)
		if err != nil {
			continue
		}
		events = append(events, icalToEvent(ics, o.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var in eventJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	uid, err := storeEvent(st, in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	in.UID = uid
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	var in eventJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if old, err := strconv.ParseInt(r.PathValue("uid"), 10, 64); err == nil {
		_ = st.DeleteObject(old)
	}
	uid, err := storeEvent(st, in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	in.UID = uid
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
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
	if err := st.DeleteObject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete event"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetCalendars returns the single default calendar.
func (s *Server) handleGetCalendars(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendars": []map[string]any{
		{"id": "calendar", "name": "Calendar", "color": "#2563eb", "primary": true},
	}})
}

func storeEvent(st *objectstore.Store, e eventJSON) (string, error) {
	if e.UID == "" {
		e.UID = randomHex() + "@hermex"
	}
	msg, err := oxcical.Import(buildICal(e), oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return "", err
	}
	id, err := st.CreateMessage(mapi.PrivateFIDCalendar, msg)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}
