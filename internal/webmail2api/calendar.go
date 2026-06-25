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

// eventJSON is the SPA's CalendarEvent shape (subset honored). CalendarID names
// the calendar (objectstore appointment folder) the event lives in; an empty or
// "calendar" value is the built-in default calendar.
type eventJSON struct {
	UID         string `json:"uid"`
	CalendarID  string `json:"calendarId,omitempty"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Start       string `json:"start"`
	End         string `json:"end,omitempty"`
	AllDay      bool   `json:"allDay,omitempty"`
}

// calendarJSON is the SPA's Calendar shape. ID is the stable "calendar" for the
// built-in calendar, otherwise the appointment folder's numeric id as a string.
type calendarJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// webmailNamespace is hermEX webmail's private named-property GUID namespace.
var webmailNamespace = mapi.GUID{Data1: 0x7B3E9A10, Data2: 0x2C4D, Data3: 0x4F6A, Data4: [8]byte{0x9B, 0x1E, 0x8D, 0x2C, 0x5A, 0x7F, 0x0E, 0x31}}

// nameCalendarColor is the named property that holds a calendar's display color.
var nameCalendarColor = mapi.PropertyName{Kind: mapi.MnidString, GUID: webmailNamespace, Name: "CalendarColor"}

// calendarColorTag resolves the per-calendar color named property to a PtUnicode
// tag for this store, allocating its id when create is set (idempotent).
func calendarColorTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{nameCalendarColor})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode)), nil
}

// propStr reads a string property value from a property bag.
func propStr(pv mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := pv.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// calendarFolderID maps a SPA calendar id to its objectstore folder id. The
// default calendar keeps the stable id "calendar" -> PrivateFIDCalendar; any
// other calendar is a folder whose numeric id is its calendar id. An unparseable
// id falls back to the default rather than failing the request.
func calendarFolderID(calendarID string) int64 {
	if calendarID == "" || calendarID == "calendar" {
		return mapi.PrivateFIDCalendar
	}
	if id, err := strconv.ParseInt(calendarID, 10, 64); err == nil {
		return id
	}
	return mapi.PrivateFIDCalendar
}

// colorOf reads a folder's stored calendar color, or "" when none is set.
func colorOf(st *objectstore.Store, folderID int64, colorTag mapi.PropTag) string {
	if colorTag == 0 {
		return ""
	}
	props, err := st.GetFolderProperties(folderID, colorTag)
	if err != nil {
		return ""
	}
	return propStr(props, colorTag)
}

// listCalendars enumerates the mailbox's calendars: the built-in Calendar (the
// stable id "calendar", always the default) plus every folder whose container
// class is IPF.Appointment. Color comes from webmail's per-calendar named prop.
func listCalendars(st *objectstore.Store) []calendarJSON {
	colorTag, _ := calendarColorTag(st, true)
	defName := "Calendar"
	if props, err := st.GetFolderProperties(mapi.PrivateFIDCalendar, mapi.PrDisplayName); err == nil {
		if n := propStr(props, mapi.PrDisplayName); n != "" {
			defName = n
		}
	}
	out := []calendarJSON{{
		ID:        "calendar",
		Name:      defName,
		Color:     colorOf(st, mapi.PrivateFIDCalendar, colorTag),
		IsDefault: true,
	}}
	folders, err := st.ListFolders()
	if err != nil {
		return out
	}
	for _, f := range folders {
		if f.ID == mapi.PrivateFIDCalendar {
			continue // the default, already added
		}
		props, err := st.GetFolderProperties(f.ID, mapi.PrContainerClass)
		if err != nil || !strings.EqualFold(propStr(props, mapi.PrContainerClass), mapi.ContainerClassAppointment) {
			continue
		}
		out = append(out, calendarJSON{
			ID:    strconv.FormatInt(f.ID, 10),
			Name:  f.DisplayName,
			Color: colorOf(st, f.ID, colorTag),
		})
	}
	return out
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

// handleGetEvents returns every event across all of the mailbox's calendars, each
// tagged with its calendarId so the SPA can filter and color per calendar.
func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	opt := oxcical.Options{Resolver: st.GetNamedPropIDs}
	events := make([]eventJSON, 0)
	for _, cal := range listCalendars(st) {
		objs, err := st.ListFolderObjects(calendarFolderID(cal.ID))
		if err != nil {
			continue
		}
		for _, o := range objs {
			msg, err := st.OpenMessage(o.ID)
			if err != nil {
				continue
			}
			ics, err := oxcical.Export(msg, opt)
			if err != nil {
				continue
			}
			e := icalToEvent(ics, o.ID)
			e.CalendarID = cal.ID
			events = append(events, e)
		}
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
	uid, err := storeEvent(st, in, calendarFolderID(in.CalendarID))
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
	// Delete by message id (folder-agnostic) then re-create in the target calendar,
	// so editing an event - including moving it to another calendar - just works.
	if old, err := strconv.ParseInt(r.PathValue("uid"), 10, 64); err == nil {
		_ = st.DeleteObject(old)
	}
	uid, err := storeEvent(st, in, calendarFolderID(in.CalendarID))
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

// handleGetCalendars lists the mailbox's calendars.
func (s *Server) handleGetCalendars(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	writeJSON(w, http.StatusOK, map[string]any{"calendars": listCalendars(st)})
}

// handleCreateCalendar creates a new calendar as an appointment folder.
func (s *Server) handleCreateCalendar(w http.ResponseWriter, r *http.Request) {
	var in calendarJSON
	if err := decodeJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	fid, err := st.CreateFolder(nil, in.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create calendar"})
		return
	}
	// CreateFolder seeds IPF.Note; overwrite the container class so the folder is a
	// real calendar, and store the chosen color.
	props := mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: mapi.ContainerClassAppointment}}
	if colorTag, _ := calendarColorTag(st, true); colorTag != 0 && in.Color != "" {
		props = append(props, mapi.TaggedPropVal{Tag: colorTag, Value: in.Color})
	}
	if err := st.SetFolderProperties(fid, props); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not configure calendar"})
		return
	}
	writeJSON(w, http.StatusOK, calendarJSON{ID: strconv.FormatInt(fid, 10), Name: in.Name, Color: in.Color})
}

// handleUpdateCalendar renames and recolors a calendar (PATCH; fields are optional).
func (s *Server) handleUpdateCalendar(w http.ResponseWriter, r *http.Request) {
	var in calendarJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id := r.PathValue("id")
	fid := calendarFolderID(id)
	if strings.TrimSpace(in.Name) != "" {
		if err := st.SetFolderName(fid, in.Name); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not rename calendar"})
			return
		}
	}
	if in.Color != "" {
		if colorTag, _ := calendarColorTag(st, true); colorTag != 0 {
			if err := st.SetFolderProperties(fid, mapi.PropertyValues{{Tag: colorTag, Value: in.Color}}); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not recolor calendar"})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, calendarJSON{ID: id, Name: in.Name, Color: in.Color, IsDefault: id == "calendar"})
}

// handleDeleteCalendar deletes a calendar and its events. The built-in default
// calendar cannot be deleted.
func (s *Server) handleDeleteCalendar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	fid := calendarFolderID(id)
	if fid == mapi.PrivateFIDCalendar {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot delete the default calendar"})
		return
	}
	// Delete the calendar's events first so DeleteFolder leaves no orphaned messages.
	if objs, err := st.ListFolderObjects(fid); err == nil {
		for _, o := range objs {
			_ = st.DeleteObject(o.ID)
		}
	}
	if err := st.DeleteFolder(fid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete calendar"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func storeEvent(st *objectstore.Store, e eventJSON, folderID int64) (string, error) {
	if e.UID == "" {
		e.UID = randomHex() + "@hermex"
	}
	msg, err := oxcical.Import(buildICal(e), oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return "", err
	}
	id, err := st.CreateMessage(folderID, msg)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}
