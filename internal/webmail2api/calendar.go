package webmail2api

import (
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// eventJSON is the SPA's CalendarEvent shape (subset honored). CalendarID names
// the calendar (objectstore appointment folder) the event lives in; an empty or
// "calendar" value is the built-in default calendar. ReminderMinutes is the
// reminder lead time in minutes (a nil/zero value means no reminder); it round-
// trips through oxcical's VALARM (NameReminderSet/NameReminderDelta named props).
// BusyStatus is PidLidBusyStatus (0=free, 1=tentative, 2=busy, 3=oof,
// 4=working elsewhere); it is set
// and read directly as a named prop because the iCal TRANSP/STATUS path oxcical
// uses cannot express all four values (oof has no iCal mapping), so the SPA's
// choice is authoritative, not the iCal-derived default.
type eventJSON struct {
	UID               string               `json:"uid"`
	CalendarID        string               `json:"calendarId,omitempty"`
	Summary           string               `json:"summary"`
	Description       string               `json:"description,omitempty"`
	Location          string               `json:"location,omitempty"`
	Start             string               `json:"start"`
	End               string               `json:"end,omitempty"`
	AllDay            bool                 `json:"allDay,omitempty"`
	ReminderMinutes   *int                 `json:"reminderMinutes,omitempty"`
	BusyStatus        *int                 `json:"busyStatus,omitempty"`
	Sensitivity       *int                 `json:"sensitivity,omitempty"`       // PR_SENSITIVITY: 0=normal, 2=private, 3=confidential (round-trips via iCal CLASS)
	Categories        []string             `json:"categories,omitempty"`        // PidNameKeywords, the shared category list (store GetCategories/SetCategories)
	Attendees         []string             `json:"attendees,omitempty"`         // smtp addresses; emitted as ATTENDEE so oxcical stores them as recipients
	OptionalAttendees []string             `json:"optionalAttendees,omitempty"` // optional attendees (ROLE=OPT-PARTICIPANT); required default for Attendees
	SendInvite        bool                 `json:"sendInvite,omitempty"`        // when true on create, email a METHOD:REQUEST iTIP invite to the attendees
	Tracking          []attendeeStatusJSON `json:"tracking,omitempty"`          // per-attendee response status (the organizer's TrackingTab), read from the recipients' PidLidResponseStatus
}

// attendeeStatusJSON is one attendee's response status for the organizer's
// TrackingTab: 0=none, 2=tentative, 3=accepted, 4=declined (PidLidResponseStatus).
type attendeeStatusJSON struct {
	Email    string `json:"email"`
	Response int    `json:"response"`
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

// propInt32 reads an int32 property value from a property bag.
func propInt32(pv mapi.PropertyValues, tag mapi.PropTag) (int32, bool) {
	if v, ok := pv.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return n, true
		}
	}
	return 0, false
}

// busyStatusTag resolves PidLidBusyStatus (NameBusyStatus) to a PtLong tag for
// this store, allocating its id when create is set (idempotent).
func busyStatusTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameBusyStatus})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtLong)), nil
}

// responseStatusTag resolves PidLidResponseStatus (NameResponseStatus) to a PtLong
// tag for this store, allocating its id when create is set (idempotent). It is the
// per-recipient prop the organizer's TrackingTab reads.
func responseStatusTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameResponseStatus})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtLong)), nil
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

// headerSafe flattens CR and LF to spaces so a user-supplied value cannot inject
// extra RFC 5322 header lines (or terminate the header block) when written onto a
// header line. It matches how oxcmail's structured export sanitizes header fields.
func headerSafe(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// icalText escapes a value for an iCalendar TEXT field per RFC 5545 3.3.11:
// backslash, comma and semicolon are backslash-escaped, a newline becomes the
// literal \n, and a bare CR is dropped. This keeps a user-supplied SUMMARY or
// LOCATION from breaking out of its content line and injecting extra properties.
func icalText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', ',', ';':
			b.WriteByte('\\')
			b.WriteByte(s[i])
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			// drop bare CR; the \n handling carries the line break
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// buildICal renders a minimal VEVENT for the proven oxcical import path.
func buildICal(e eventJSON) []byte {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//webmail2//EN\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", e.UID)
	fmt.Fprintf(&b, "SUMMARY:%s\r\n", icalText(e.Summary))
	fmt.Fprintf(&b, "DTSTART%s\r\n", toICalTime(e.Start, e.AllDay))
	if e.End != "" {
		fmt.Fprintf(&b, "DTEND%s\r\n", toICalTime(e.End, e.AllDay))
	}
	for _, a := range e.Attendees {
		fmt.Fprintf(&b, "ATTENDEE;CN=%s;ROLE=REQ-PARTICIPANT:mailto:%s\r\n", a, a)
	}
	for _, a := range e.OptionalAttendees {
		fmt.Fprintf(&b, "ATTENDEE;CN=%s;ROLE=OPT-PARTICIPANT:mailto:%s\r\n", a, a)
	}
	if e.Description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", icalText(e.Description))
	}
	if e.Location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\r\n", icalText(e.Location))
	}
	if e.ReminderMinutes != nil && *e.ReminderMinutes > 0 {
		b.WriteString("BEGIN:VALARM\r\nACTION:DISPLAY\r\n")
		fmt.Fprintf(&b, "TRIGGER:-PT%dM\r\n", *e.ReminderMinutes)
		b.WriteString("END:VALARM\r\n")
	}
	// Sensitivity maps to iCalendar CLASS (PRIVATE for private/personal, CONFIDENTIAL
	// for confidential); oxcical's import maps CLASS back to PR_SENSITIVITY.
	if e.Sensitivity != nil {
		switch *e.Sensitivity {
		case 1, 2:
			b.WriteString("CLASS:PRIVATE\r\n")
		case 3:
			b.WriteString("CLASS:CONFIDENTIAL\r\n")
		}
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
	if v, _ := icalProp(ics, "TRIGGER"); v != "" {
		// A VALARM trigger is an iCal duration like "-PT15M"; the lead time in
		// minutes is its absolute value (oxcical emits "-PT<n>M").
		if mins, ok := icalDurationMinutes(v); ok && mins > 0 {
			e.ReminderMinutes = &mins
		}
	}
	if c, _ := icalProp(ics, "CLASS"); c != "" {
		// CLASS maps back to PR_SENSITIVITY (PRIVATE⇒private, CONFIDENTIAL⇒confidential);
		// PUBLIC/absent stays the default normal, so no Sensitivity is set.
		switch strings.ToUpper(strings.TrimSpace(c)) {
		case "PRIVATE":
			s := 2
			e.Sensitivity = &s
		case "CONFIDENTIAL":
			s := 3
			e.Sensitivity = &s
		}
	}
	return e
}

// icalDurationMinutes parses a simple iCal duration of the form (-)PT<n>M and
// returns its absolute minutes. It is the inverse of buildICal's VALARM TRIGGER,
// kept here so icalToEvent can recover the reminder lead time without pulling in
// the full oxcical duration parser.
func icalDurationMinutes(v string) (int, bool) {
	v = strings.TrimSpace(v)
	neg := false
	if strings.HasPrefix(v, "-") {
		neg = true
		v = v[1:]
	} else if strings.HasPrefix(v, "+") {
		v = v[1:]
	}
	if !strings.HasPrefix(v, "PT") {
		return 0, false
	}
	rest := v[2:]
	// Only the minutes form ("-PT<n>M") is produced here; reject anything else.
	if !strings.HasSuffix(rest, "M") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(rest, "M"))
	if err != nil || n < 0 {
		return 0, false
	}
	_ = neg
	return n, true
}

// handleGetEvents returns every event across all of the mailbox's calendars, each
// tagged with its calendarId so the SPA can filter and color per calendar.
func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	// Optional [start,end) window. When both params parse, an object is pruned by
	// its cheap start/end named props before the costly per-object iCal export
	// (mirrors today.go's appointmentsOn). Absent or malformed params keep the
	// full-calendar export so an unbounded client is never silently truncated.
	winStart, winEnd, windowed := eventWindow(r)
	var winStartTag, winEndTag mapi.PropTag
	if windowed {
		if ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole}); err == nil && len(ids) == 2 {
			winStartTag = mapi.MakeTag(ids[0], mapi.PtSysTime)
			winEndTag = mapi.MakeTag(ids[1], mapi.PtSysTime)
		} else {
			windowed = false // cannot resolve the time tags; fail open to full export
		}
	}
	opt := oxcical.Options{Resolver: st.GetNamedPropIDs}
	events := make([]eventJSON, 0)
	// busyTag resolves PidLidBusyStatus once; an absent tag (fresh store) means the
	// busy status is read from the iCal-derived default and stays nil.
	busyTag, _ := busyStatusTag(st, false)
	// respTag resolves PidLidResponseStatus once; the organizer's TrackingTab reads
	// each attendee's response from its recipient row.
	respTag, _ := responseStatusTag(st, false)
	for _, cal := range listCalendars(st) {
		fid := calendarFolderID(cal.ID)
		var objs []objectstore.FolderObject
		var err error
		if windowed {
			// The store applies the window, so the export below runs only for the
			// objects that survive it. Reading each object's properties back to
			// compare two times costs a query per object and grows with the calendar,
			// not with the answer.
			objs, err = st.ListFolderObjectsInWindow(fid, winStartTag, winEndTag, winStart, winEnd)
		} else {
			objs, err = st.ListFolderObjects(fid)
		}
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
			// The SPA addresses an event by its message id (delete and update parse
			// it back to a store id); the iCalendar UID is the meeting identity, not
			// a store handle, so the message id - not icalToEvent's UID - is surfaced.
			e.UID = strconv.FormatInt(o.ID, 10)
			e.CalendarID = cal.ID
			// BusyStatus is read directly from the named prop so every value
			// survives the reload; the exported iCal can only say opaque or
			// transparent, so tentative, oof and working-elsewhere are lost there.
			// An appointment migrated from Exchange commonly carries 4.
			if busyTag != 0 {
				if pv, err := st.GetMessageProperties(o.ID, busyTag); err == nil {
					if bs, ok := propInt32(pv, busyTag); ok {
						n := int(bs)
						e.BusyStatus = &n
					}
				}
			}
			// Categories are the shared PidNameKeywords list, read directly.
			if cats, err := st.GetCategories(o.ID); err == nil && len(cats) > 0 {
				e.Categories = cats
			}
			// Attendees + Tracking: each recipient's SMTP address is the attendee
			// list (oxcical stores ATTENDEE as recipients), and its PidLidResponseStatus
			// is the organizer's tracking view (populated by inbound REPLY).
			if recips, err := st.ListRecipients(o.ID); err == nil {
				for _, r := range recips {
					if r.SmtpAddress == "" {
						continue
					}
					e.Attendees = append(e.Attendees, r.SmtpAddress)
					if respTag != 0 {
						resp := 0
						if pv, err := st.GetRecipientProperties(r.ID, respTag); err == nil {
							if v, ok := propInt32(pv, respTag); ok {
								resp = int(v)
							}
						}
						e.Tracking = append(e.Tracking, attendeeStatusJSON{Email: r.SmtpAddress, Response: resp})
					}
				}
			}
			events = append(events, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// eventWindow parses the optional start/end RFC3339 query params of the events
// list request. windowed is true only when both parse and start precedes end; a
// partial or malformed range falls back to the full-calendar export so an
// unbounded client is never silently truncated.
func eventWindow(r *http.Request) (start, end time.Time, windowed bool) {
	q := r.URL.Query()
	s, err1 := time.Parse(time.RFC3339, q.Get("start"))
	e, err2 := time.Parse(time.RFC3339, q.Get("end"))
	if err1 != nil || err2 != nil || !s.Before(e) {
		return time.Time{}, time.Time{}, false
	}
	return s, e, true
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
		logError("calendar-save", err, logging.Fields{"calendar": in.CalendarID})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	in.UID = uid
	// An organizer who sends an invite emails a METHOD:REQUEST iTIP message to the
	// attendees; the recipients' clients (and hermEX's own RSVP path) offer
	// accept/tentative/decline. Best-effort: a delivery failure still leaves the
	// event saved, and the in.UID carries the meeting identity.
	if in.SendInvite && len(in.Attendees) > 0 {
		c, ok := s.session(r)
		organizer := ""
		if ok {
			organizer = c.Email
		}
		in.UID = uidOrGenerated(in.UID)
		if raw, recipients, berr := buildMeetingRequest(organizer, in); berr == nil && organizer != "" {
			if _, derr := mta.DeliverAndRelay(s.accounts, s.spool, organizer, recipients, raw, time.Now()); derr == nil {
				// File a Sent copy so the organizer sees the outgoing invite.
				fileSentCopy(st, raw, organizer, "meeting-request")
			}
		}
	}
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
	// An organizer editing a meeting can resend the METHOD:REQUEST so invitees see
	// the new time/details (SendInvite on update). Best-effort like the create path.
	if in.SendInvite && len(in.Attendees) > 0 {
		if c, ok := s.session(r); ok {
			in.UID = uidOrGenerated(in.UID)
			if raw, recipients, berr := buildMeetingRequest(c.Email, in); berr == nil {
				if _, derr := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, recipients, raw, time.Now()); derr == nil {
					fileSentCopy(st, raw, c.Email, "meeting-update")
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, in)
}

// handleExportEvent streams a single calendar event as an iCalendar (.ics)
// download, the attach-item-calendar surface: the compose editor embeds it as a
// text/calendar attachment so a recipient imports the event into their calendar.
// It reuses oxcical.Export (the canonical CalDAV path), so the bytes a recipient
// receives are byte-identical to what CalDAV/EAS/EWS see.
func (s *Server) handleExportEvent(w http.ResponseWriter, r *http.Request) {
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
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such event"})
		return
	}
	ics, err := oxcical.Export(msg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not export"})
		return
	}
	filename := filenameFrom(propString(msg, mapi.PrSubject))
	if filename == "" {
		filename = "event"
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`.ics"`)
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	_, _ = w.Write(ics)
}

// filenameFrom sanitizes a free-form string to a safe filename (alnum + -/_).
func filenameFrom(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
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
	// If the event had attendees (the organizer is cancelling a meeting), email a
	// METHOD:CANCEL iTIP notice before the delete so the recipients' clients drop
	// it. Best-effort: a delivery failure still lets the organizer delete their
	// own copy.
	if c, ok := s.session(r); ok {
		if recips, err := st.ListRecipients(id); err == nil {
			var addrs []string
			for _, rc := range recips {
				if rc.SmtpAddress != "" {
					addrs = append(addrs, rc.SmtpAddress)
				}
			}
			if len(addrs) > 0 {
				summary := ""
				if pv, err := st.GetMessageProperties(id, mapi.PrSubject); err == nil {
					summary = propStr(pv, mapi.PrSubject)
				}
				if raw, rec, berr := buildCancellationRequest(c.Email, eventJSON{UID: strconv.FormatInt(id, 10), Summary: summary, Attendees: addrs}); berr == nil {
					if _, derr := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, rec, raw, time.Now()); derr != nil {
						logError("send-meeting-cancellation", derr, logging.Fields{"user": c.Email})
					}
					fileSentCopy(st, raw, c.Email, "meeting-cancellation")
				}
			}
		}
	}
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

// buildMeetingRequest renders a METHOD:REQUEST iTIP invite wrapped in an RFC 5322
// MIME message addressed to the attendees. The body is a plain-text summary plus
// the full iCalendar as a text/calendar;method=REQUEST part, the shape an invitee
// client parses to offer accept/tentative/decline (the RSVP path hermEX already
// serves). The organizer is the authenticated sender. It returns the raw MIME and
// the deduplicated attendee address list (the recipients).
func buildMeetingRequest(organizer string, e eventJSON) ([]byte, []string, error) {
	// Recipients: dedup the attendee addresses (required + optional), parsed to
	// bare smtp; optionalSet marks the OPT-PARTICIPANT roles.
	recipients := make([]string, 0, len(e.Attendees)+len(e.OptionalAttendees))
	optionalSet := map[string]bool{}
	seen := map[string]bool{}
	add := func(list []string, optional bool) {
		for _, a := range list {
			// Drop what does not parse rather than carrying the raw string forward.
			// It reaches the invite's To header and the iCal ATTENDEE line, so an
			// entry holding a line break would splice headers of the organizer's
			// choosing into a message the server relays externally, or end the header
			// block and push the rest into the body.
			parsed, err := mail.ParseAddress(strings.TrimSpace(a))
			if err != nil {
				continue
			}
			addr := strings.ToLower(parsed.Address)
			if addr == "" || seen[addr] || addr == strings.ToLower(organizer) {
				continue
			}
			seen[addr] = true
			if optional {
				optionalSet[addr] = true
			}
			recipients = append(recipients, addr)
		}
	}
	add(e.Attendees, false)
	add(e.OptionalAttendees, true)
	if len(recipients) == 0 {
		return nil, nil, fmt.Errorf("no attendees")
	}
	// The iCalendar: METHOD:REQUEST at the VCALENDAR level, ORGANIZER set, and an
	// ATTENDEE per recipient (RSVP=TRUE so the invitee client offers a response).
	var cal strings.Builder
	cal.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//webmail2//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&cal, "UID:%s\r\n", uidOrGenerated(e.UID))
	fmt.Fprintf(&cal, "SUMMARY:%s\r\n", icalText(e.Summary))
	fmt.Fprintf(&cal, "DTSTART%s\r\n", toICalTime(e.Start, e.AllDay))
	if e.End != "" {
		fmt.Fprintf(&cal, "DTEND%s\r\n", toICalTime(e.End, e.AllDay))
	}
	if e.Location != "" {
		fmt.Fprintf(&cal, "LOCATION:%s\r\n", icalText(e.Location))
	}
	fmt.Fprintf(&cal, "ORGANIZER;CN=%s:mailto:%s\r\n", organizer, organizer)
	for _, a := range recipients {
		role := "REQ-PARTICIPANT"
		if optionalSet[a] {
			role = "OPT-PARTICIPANT"
		}
		fmt.Fprintf(&cal, "ATTENDEE;CN=%s;ROLE=%s;RSVP=TRUE:mailto:%s\r\n", a, role, a)
	}
	cal.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")

	// A plain-text body the invitee reads if their client does not render the
	// calendar part.
	parts := []string{
		e.Summary,
		"",
		"When: " + e.Start,
	}
	if e.End != "" {
		parts[len(parts)-1] = "When: " + e.Start + " - " + e.End
	}
	if e.Location != "" {
		parts = append(parts, "Where: "+e.Location)
	}
	if e.Description != "" {
		parts = append(parts, "", e.Description)
	}
	textBody := strings.Join(parts, "\r\n")

	boundary := "hermex-invite-" + randomHex()
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", organizer)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", headerSafe(e.Summary))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@hermex>\r\n", randomHex())
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(textBody)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(cal.String())
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String()), recipients, nil
}

// buildCancellationRequest renders a METHOD:CANCEL iTIP message (RFC 5546 §3.7):
// the organizer cancels a meeting, telling each attendee it is off. The iCalendar
// carries the original UID with STATUS:CANCELLED; the MIME body is a plain-text
// notice plus the text/calendar;method=CANCEL part. Returns the raw message and
// the deduplicated attendee address list.
func buildCancellationRequest(organizer string, e eventJSON) ([]byte, []string, error) {
	recipients := make([]string, 0, len(e.Attendees))
	seen := map[string]bool{}
	for _, a := range e.Attendees {
		addr := strings.TrimSpace(a)
		if parsed, err := mail.ParseAddress(addr); err == nil {
			addr = parsed.Address
		}
		addr = strings.ToLower(addr)
		if addr == "" || seen[addr] || addr == strings.ToLower(organizer) {
			continue
		}
		seen[addr] = true
		recipients = append(recipients, addr)
	}
	if len(recipients) == 0 {
		return nil, nil, fmt.Errorf("no attendees")
	}
	var cal strings.Builder
	cal.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//webmail2//EN\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\n")
	fmt.Fprintf(&cal, "UID:%s\r\n", uidOrGenerated(e.UID))
	fmt.Fprintf(&cal, "SUMMARY:%s\r\n", icalText(e.Summary))
	fmt.Fprintf(&cal, "DTSTART%s\r\n", toICalTime(e.Start, e.AllDay))
	fmt.Fprintf(&cal, "STATUS:CANCELLED\r\n")
	fmt.Fprintf(&cal, "ORGANIZER;CN=%s:mailto:%s\r\n", organizer, organizer)
	for _, a := range recipients {
		fmt.Fprintf(&cal, "ATTENDEE;CN=%s:mailto:%s\r\n", a, a)
	}
	cal.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	textBody := fmt.Sprintf("Cancelled: %s", e.Summary)
	boundary := "hermex-cancel-" + randomHex()
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", organizer)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&b, "Subject: Cancelled: %s\r\n", headerSafe(e.Summary))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@hermex>\r\n", randomHex())
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(textBody)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/calendar; method=CANCEL; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(cal.String())
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String()), recipients, nil
}

// uidOrGenerated returns the event's UID, minting one when empty so an invite
// always carries a stable meeting identity.
func uidOrGenerated(uid string) string {
	if uid != "" {
		return uid
	}
	return randomHex() + "@hermex"
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
	// BusyStatus is set directly as a named prop because the iCal TRANSP/STATUS
	// path oxcical imports from cannot express all four values (oof has no iCal
	// mapping); the SPA's choice is authoritative, so override the import default.
	if e.BusyStatus != nil && fitsMAPILong(*e.BusyStatus) {
		if tag, err := busyStatusTag(st, true); err == nil && tag != 0 {
			var props mapi.PropertyValues
			// #nosec G115 -- the fitsMAPILong guard on the same line refuses a value the property cannot carry
			props.Set(tag, int32(*e.BusyStatus))
			_ = st.SetMessageProperties(id, props)
		}
	}
	// Categories ride the shared PidNameKeywords named prop (the same list every
	// protocol reads), set directly rather than through the iCal CATEGORIES path
	// oxcical's VEVENT does not parse.
	if len(e.Categories) > 0 {
		_ = st.SetCategories(id, e.Categories)
	}
	return strconv.FormatInt(id, 10), nil
}
