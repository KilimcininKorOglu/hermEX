package webmail2api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
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
	Recurrence        string               `json:"recurrence,omitempty"`        // the RRULE value, e.g. "FREQ=WEEKLY"
	Timezone          string               `json:"timezone,omitempty"`          // IANA zone a timed event's wall clock is kept in
	// Occurrence, SeriesStart and SeriesEnd are set on the rows a windowed listing
	// expands a series into: Occurrence is the instance's generated instant (its
	// RECURRENCE-ID), and SeriesStart and SeriesEnd are the series' own first span,
	// which is what an edit of the whole series starts from.
	Occurrence  string `json:"occurrence,omitempty"`
	SeriesStart string `json:"seriesStart,omitempty"`
	SeriesEnd   string `json:"seriesEnd,omitempty"`
	// Canceled marks a meeting, or one instance of a series, its organizer
	// called off. The item stays until the user removes it, and it occupies no
	// time. It is read-only: the server sets it from the delivered cancellation.
	Canceled bool `json:"canceled,omitempty"`
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
	// Description is the folder comment (PidTagComment). On an update, nil
	// leaves the stored description alone and "" clears it.
	Description *string `json:"description,omitempty"`
}

// descriptionOf reads a calendar folder's description, nil when it has none.
func descriptionOf(st *objectstore.Store, folderID int64) *string {
	props, err := st.GetFolderProperties(folderID, mapi.PrComment)
	if err != nil {
		return nil
	}
	if d := propStr(props, mapi.PrComment); d != "" {
		return &d
	}
	return nil
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
	// A color tag that cannot be resolved shows every calendar uncolored; the
	// list itself is still right, so the failure is recorded, not answered.
	colorTag, err := calendarColorTag(st, true)
	if err != nil {
		logError("calendar-color-tag", err, logging.Fields{})
	}
	defName := "Calendar"
	if props, err := st.GetFolderProperties(mapi.PrivateFIDCalendar, mapi.PrDisplayName); err == nil {
		if n := propStr(props, mapi.PrDisplayName); n != "" {
			defName = n
		}
	}
	out := []calendarJSON{{
		ID:          "calendar",
		Name:        defName,
		Color:       colorOf(st, mapi.PrivateFIDCalendar, colorTag),
		IsDefault:   true,
		Description: descriptionOf(st, mapi.PrivateFIDCalendar),
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
			ID:          strconv.FormatInt(f.ID, 10),
			Name:        f.DisplayName,
			Color:       colorOf(st, f.ID, colorTag),
			Description: descriptionOf(st, f.ID),
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
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', ',', ';':
			out = append(out, '\\', s[i])
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			// drop bare CR; the \n handling carries the line break
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}

// buildICal renders a minimal VEVENT for the proven oxcical import path. organizer
// names the meeting's organizer (empty for an appointment, or for a meeting
// organized before this webmail recorded one), and seq is the meeting revision.
func buildICal(e eventJSON, organizer string, seq int) []byte {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//hermEX//webmail2//EN"}
	loc := eventLocation(e)
	lines = append(lines, vTimezoneLines(loc, e.Start)...)
	lines = append(lines,
		"BEGIN:VEVENT",
		"UID:"+e.UID,
		"SUMMARY:"+icalText(e.Summary),
		"DTSTART"+icalTimeIn(e.Start, e.AllDay, loc))
	if e.End != "" {
		lines = append(lines, "DTEND"+icalTimeIn(e.End, e.AllDay, loc))
	}
	if validRRule(e.Recurrence) {
		lines = append(lines, "RRULE:"+e.Recurrence)
	}
	lines = append(lines, "SEQUENCE:"+strconv.Itoa(seq))
	if organizer != "" {
		lines = append(lines, "ORGANIZER:mailto:"+organizer)
	}
	lines = append(lines, attendeeLines(e)...)
	lines = appendICalText(lines, "DESCRIPTION:", e.Description)
	lines = appendICalText(lines, "LOCATION:", e.Location)
	lines = append(lines, alarmLines(e.ReminderMinutes)...)
	text := strings.Join(lines, "\r\n") + "\r\n"
	return []byte(text + icalClass(e.Sensitivity) + "END:VEVENT\r\nEND:VCALENDAR\r\n")
}

// attendeeLines returns an ATTENDEE line for each attendee that parses as an
// address, required ones first. An optional attendee who is also required is
// written once, as required.
func attendeeLines(e eventJSON) []string {
	var out []string
	required := cleanAddresses(e.Attendees)
	for _, a := range required {
		out = append(out, "ATTENDEE;CN="+a+";ROLE=REQ-PARTICIPANT;RSVP=TRUE:mailto:"+a)
	}
	for _, a := range cleanAddresses(e.OptionalAttendees) {
		if !slices.Contains(required, a) {
			out = append(out, "ATTENDEE;CN="+a+";ROLE=OPT-PARTICIPANT;RSVP=TRUE:mailto:"+a)
		}
	}
	return out
}

// appendICalText appends one escaped text property, skipping an empty value.
func appendICalText(lines []string, name, value string) []string {
	if value == "" {
		return lines
	}
	return append(lines, name+icalText(value))
}

// alarmLines returns the display VALARM for a reminder set to a positive lead
// time.
func alarmLines(minutes *int) []string {
	if minutes == nil || *minutes <= 0 {
		return nil
	}
	return []string{"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT" + strconv.Itoa(*minutes) + "M", "END:VALARM"}
}

// icalClass maps sensitivity to the iCalendar CLASS line (PRIVATE for
// private/personal, CONFIDENTIAL for confidential); oxcical's import maps CLASS
// back to PR_SENSITIVITY.
func icalClass(sensitivity *int) string {
	if sensitivity == nil {
		return ""
	}
	switch *sensitivity {
	case 1, 2:
		return "CLASS:PRIVATE\r\n"
	case 3:
		return "CLASS:CONFIDENTIAL\r\n"
	}
	return ""
}

// icalValue returns a property's value in the first VEVENT, without its
// parameters.
func icalValue(ics []byte, name string) string {
	return icalProp(ics, name).value
}

// icalLine is one property read from a VEVENT: its value and the part of the
// key after its name (the parameters).
type icalLine struct {
	value  string
	params string
}

// icalProp returns a property's value in the first VEVENT (its alarm included)
// and the part of the key after its name (the parameters), ignoring folding. Lines
// outside the event are skipped, because a VTIMEZONE ahead of it carries DTSTART
// lines of its own.
func icalProp(ics []byte, name string) icalLine {
	inEvent := false
	for line := range strings.SplitSeq(string(ics), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.EqualFold(line, "BEGIN:VEVENT") {
			inEvent = true
			continue
		}
		if !inEvent {
			continue
		}
		if strings.EqualFold(line, "END:VEVENT") {
			break
		}
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		base, params := key, ""
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			base, params = key[:semi], key[semi:]
		}
		if strings.EqualFold(base, name) {
			return icalLine{value: val, params: params}
		}
	}
	return icalLine{}
}

func icalToEvent(ics []byte, id int64) eventJSON {
	e := eventJSON{UID: strconv.FormatInt(id, 10)}
	if v := icalValue(ics, "UID"); v != "" {
		e.UID = v
	}
	e.Summary = icalValue(ics, "SUMMARY")
	e.Description = icalValue(ics, "DESCRIPTION")
	e.Location = icalValue(ics, "LOCATION")
	e.Recurrence = icalValue(ics, "RRULE")
	icalSpan(ics, &e)
	if v := icalValue(ics, "TRIGGER"); v != "" {
		// A VALARM trigger is an iCal duration like "-PT15M"; the lead time in
		// minutes is its absolute value (oxcical emits "-PT<n>M").
		if mins, ok := icalDurationMinutes(v); ok && mins > 0 {
			e.ReminderMinutes = &mins
		}
	}
	if c := icalValue(ics, "CLASS"); c != "" {
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
	if strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+") {
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
	return n, true
}

// handleGetEvents returns every event across all of the mailbox's calendars, each
// tagged with its calendarId so the SPA can filter and color per calendar.
func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	// Optional [start,end) window. When both params parse, an object is pruned by
	// its cheap start/end named props before the costly per-object iCal export
	// (mirrors today.go's appointmentsOn). Absent or malformed params keep the
	// full-calendar export so an unbounded client is never silently truncated.
	win := newEventWindowScan(st, r)
	events := make([]eventJSON, 0)
	for _, cal := range listCalendars(st) {
		objs, err := win.list(calendarFolderID(cal.ID))
		if err != nil {
			continue
		}
		for _, o := range objs {
			events = append(events, win.rows(o.ID, cal.ID)...)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// eventScan renders one calendar listing. The named-property tags it needs are
// resolved once for the whole request rather than per object.
type eventScan struct {
	st       *objectstore.Store
	opt      oxcical.Options
	busyTag  mapi.PropTag
	respTag  mapi.PropTag
	stateTag mapi.PropTag
	windowed bool
	startTag mapi.PropTag
	endTag   mapi.PropTag
	recurTag mapi.PropTag
	start    time.Time
	end      time.Time
}

// newEventWindowScan resolves the optional [start,end) window and the named
// property tags the listing reads.
//
// When both window params parse, an object is pruned by its cheap start/end
// named props before the costly per-object iCal export (mirrors today.go's
// appointmentsOn). Absent or malformed params keep the full-calendar export so
// an unbounded client is never silently truncated.
func newEventWindowScan(st *objectstore.Store, r *http.Request) *eventScan {
	sc := &eventScan{st: st, opt: oxcical.Options{Resolver: st.GetNamedPropIDs}}
	// busyTag resolves PidLidBusyStatus once; an absent tag (fresh store) means
	// the busy status is read from the iCal-derived default and stays nil.
	var err error
	if sc.busyTag, err = busyStatusTag(st, false); err != nil {
		logError("busy-status-tag", err, logging.Fields{})
	}
	// respTag resolves PidLidResponseStatus once; the organizer's TrackingTab
	// reads each attendee's response from its recipient row.
	if sc.respTag, err = responseStatusTag(st, false); err != nil {
		logError("response-status-tag", err, logging.Fields{})
	}
	// stateTag resolves PidLidAppointmentStateFlags once; its cancelled bit marks a
	// meeting its organizer called off.
	sc.stateTag = namedLongTag(st, mapi.NameAppointmentStateFlags)
	sc.start, sc.end, sc.windowed = eventWindow(r)
	if !sc.windowed {
		return sc
	}
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole, mapi.NameRecurring})
	if err != nil || len(ids) != 3 || ids[0] == 0 || ids[1] == 0 {
		sc.windowed = false // cannot resolve the time tags; fail open to full export
		return sc
	}
	sc.startTag = mapi.MakeTag(ids[0], mapi.PtSysTime)
	sc.endTag = mapi.MakeTag(ids[1], mapi.PtSysTime)
	if ids[2] != 0 {
		sc.recurTag = mapi.MakeTag(ids[2], mapi.PtBoolean)
	}
	return sc
}

// list enumerates one calendar folder, applying the window in the store when
// there is one. Reading each object's properties back to compare two times
// costs a query per object and grows with the calendar, not with the answer. A
// series master carries its first instance's times, so the series are added
// whole and the window is applied to their expanded instances instead.
func (sc *eventScan) list(fid int64) ([]objectstore.FolderObject, error) {
	if !sc.windowed {
		return sc.st.ListFolderObjects(fid)
	}
	objs, err := sc.st.ListFolderObjectsInWindow(fid, sc.startTag, sc.endTag, sc.start, sc.end)
	if err != nil || sc.recurTag == 0 {
		return objs, err
	}
	series, err := sc.st.ListFolderObjectsWithFlag(fid, sc.recurTag)
	if err != nil {
		return nil, err
	}
	return mergeFolderObjects(objs, series), nil
}

// rows renders one stored appointment: the event itself, or in a windowed
// listing each of a series' instances in the window. It is empty when the
// appointment cannot be exported.
func (sc *eventScan) rows(id int64, calendarID string) []eventJSON {
	e, ics, ok := sc.event(id, calendarID)
	if !ok {
		return nil
	}
	if !sc.windowed {
		return []eventJSON{e}
	}
	return expandedRows(e, ics, sc.start, sc.end)
}

// event renders one stored appointment and the iCalendar it was read from,
// reporting false when it cannot be exported.
func (sc *eventScan) event(id int64, calendarID string) (eventJSON, []byte, bool) {
	msg, err := sc.st.OpenMessage(id)
	if err != nil {
		return eventJSON{}, nil, false
	}
	ics, err := oxcical.Export(msg, sc.opt)
	if err != nil {
		return eventJSON{}, nil, false
	}
	e := icalToEvent(ics, id)
	if e.Timezone == "" && !e.AllDay {
		// A single event is exported in UTC, so its zone is read from the display
		// time zone import stored beside it.
		if loc := oxcical.DisplayZone(msg.Props, sc.opt); loc != nil {
			e.Timezone = loc.String()
		}
	}
	// The SPA addresses an event by its message id (delete and update parse it
	// back to a store id); the iCalendar UID is the meeting identity, not a store
	// handle, so the message id - not icalToEvent's UID - is surfaced.
	e.UID = strconv.FormatInt(id, 10)
	e.CalendarID = calendarID
	e.BusyStatus = sc.busyStatus(id)
	flags, _ := propInt32(msg.Props, sc.stateTag)
	e.Canceled = sc.stateTag != 0 && flags&asfCanceled != 0
	// Categories are the shared PidNameKeywords list, read directly.
	e.Categories = storedCategories(sc.st, id)
	sc.addAttendees(&e, id)
	return e, ics, true
}

// busyStatus reads PidLidBusyStatus directly so every value survives the
// reload: the exported iCal can only say opaque or transparent, so tentative,
// oof and working-elsewhere are lost there. An appointment migrated from
// Exchange commonly carries 4.
func (sc *eventScan) busyStatus(id int64) *int {
	if sc.busyTag == 0 {
		return nil
	}
	pv, err := sc.st.GetMessageProperties(id, sc.busyTag)
	if err != nil {
		return nil
	}
	bs, ok := propInt32(pv, sc.busyTag)
	if !ok {
		return nil
	}
	n := int(bs)
	return &n
}

// addAttendees fills the required and optional attendee lists and the
// organizer's tracking view. Each recipient's SMTP address is an attendee
// (oxcical stores ATTENDEE as recipients, an optional one as Cc), and its
// PidLidResponseStatus is the tracking view an inbound REPLY populates.
func (sc *eventScan) addAttendees(e *eventJSON, id int64) {
	recips, err := sc.st.ListRecipients(id)
	if err != nil {
		return
	}
	for _, r := range recips {
		if r.SmtpAddress == "" {
			continue
		}
		if sc.recipientType(r.ID) == mapi.RecipCc {
			e.OptionalAttendees = append(e.OptionalAttendees, r.SmtpAddress)
		} else {
			e.Attendees = append(e.Attendees, r.SmtpAddress)
		}
		if sc.respTag != 0 {
			e.Tracking = append(e.Tracking, attendeeStatusJSON{
				Email: r.SmtpAddress, Response: sc.responseStatus(r.ID),
			})
		}
	}
}

// recipientType reads one attendee's PidTagRecipientType: Cc is an optional
// attendee ([MS-OXCICAL] 2.1.3.1.1.20.2). It reports 0 when it is unreadable,
// which files the attendee as required.
func (sc *eventScan) recipientType(recipientID int64) int32 {
	pv, err := sc.st.GetRecipientProperties(recipientID, mapi.PrRecipientType)
	if err != nil {
		return 0
	}
	t, _ := propInt32(pv, mapi.PrRecipientType)
	return t
}

// responseStatus reads one attendee's PidLidResponseStatus, reporting 0 (none)
// when it is unset.
func (sc *eventScan) responseStatus(recipientID int64) int {
	pv, err := sc.st.GetRecipientProperties(recipientID, sc.respTag)
	if err != nil {
		return 0
	}
	v, ok := propInt32(pv, sc.respTag)
	if !ok {
		return 0
	}
	return int(v)
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
	in, ok := decodeEvent(w, r)
	if !ok {
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	// A meeting records its organizer, the signed-in user; an appointment with
	// nobody to meet records none.
	organizer := ""
	if c, ok := s.session(r); ok && hasAttendees(in) {
		organizer = c.Email
	}
	fid, ok := calendarOr404(w, st, in.CalendarID)
	if !ok || refuseOverQuota(w, st) {
		return
	}
	id, err := storeEvent(st, in, fid, organizer)
	if err != nil {
		logError("calendar-save", err, logging.Fields{"calendar": in.CalendarID})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	// The invitation goes out after the meeting is stored and is best-effort: a
	// delivery failure leaves the event saved.
	switch {
	case organizer == "":
	case in.SendInvite:
		s.sendInvitation(st, id, organizer, "meeting-request", in)
	default:
		markInvited(st, id, false)
	}
	in.UID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeEvent(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	if !eventEditTarget(w, st, id, in.CalendarID) {
		return
	}
	caller := ""
	if c, ok := s.session(r); ok {
		caller = c.Email
	}
	organizes, err := updateEventInPlace(st, id, in, caller)
	if errors.Is(err, errNoSuchEvent) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such event"})
		return
	}
	if err != nil {
		logError("calendar-update", err, logging.Fields{"calendar": in.CalendarID})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save event"})
		return
	}
	// An organizer editing a meeting can resend it so the attendees' clients update
	// the meeting they hold. Best-effort like the create path.
	if in.SendInvite && organizes {
		s.sendInvitation(st, id, caller, "meeting-update", in)
	}
	in.UID = strconv.FormatInt(id, 10)
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
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
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
	writeBody(w, ics)
}

// filenameFrom sanitizes a free-form string to a safe filename (alnum + -/_).
func filenameFrom(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		if c := name[i]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		}
	}
	return string(out)
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	if !objectInKind(w, st, id, inCalendar) {
		return
	}
	// The organizer's deletion cancels the meeting for its attendees; an
	// attendee's deletion of their own copy tells nobody.
	var cancel *pendingMail
	if c, ok := s.session(r); ok {
		cancel = prepareCancellation(st, id, c.Email)
	}
	if err := st.SoftDeleteObject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete event"})
		return
	}
	cancel.send(s, st)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// attendeeAddresses lists the SMTP addresses an event's recipients carry.
func attendeeAddresses(st *objectstore.Store, id int64) []string {
	recips, err := st.ListRecipients(id)
	if err != nil {
		return nil
	}
	var addrs []string
	for _, rc := range recips {
		if rc.SmtpAddress != "" {
			addrs = append(addrs, rc.SmtpAddress)
		}
	}
	return addrs
}

// handleGetCalendars lists the mailbox's calendars.
func (s *Server) handleGetCalendars(w http.ResponseWriter, r *http.Request) {
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	writeJSON(w, http.StatusOK, map[string]any{"calendars": listCalendars(st)})
}

// handleCreateCalendar creates a new calendar as an appointment folder.
func (s *Server) handleCreateCalendar(w http.ResponseWriter, r *http.Request) {
	var in calendarJSON
	if err := decodeJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	// The properties are resolved before the folder exists, so a failure leaves
	// no half-made folder behind.
	extra, err := calendarProps(st, in)
	if err != nil {
		logError("calendar-props", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not configure calendar"})
		return
	}
	fid, err := st.CreateFolder(nil, in.Name)
	if errors.Is(err, objectstore.ErrFolderExists) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a folder with that name already exists"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create calendar"})
		return
	}
	// CreateFolder seeds IPF.Note; overwrite the container class so the folder is a
	// real calendar, and store the chosen color and description.
	props := append(mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: mapi.ContainerClassAppointment}}, extra...)
	if err := st.SetFolderProperties(fid, props); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not configure calendar"})
		return
	}
	writeJSON(w, http.StatusOK, calendarJSON{ID: strconv.FormatInt(fid, 10), Name: in.Name, Color: in.Color, Description: in.Description})
}

// calendarProps returns the color and description properties a calendar
// create or update writes: the color when one is given, and the description
// when the request carries one ("" clears it).
//
// A color whose property id cannot be resolved is an error, so the caller never
// reports a color saved that was dropped.
func calendarProps(st *objectstore.Store, in calendarJSON) (mapi.PropertyValues, error) {
	var props mapi.PropertyValues
	if in.Color != "" {
		colorTag, err := calendarColorTag(st, true)
		if err != nil {
			return nil, err
		}
		if colorTag == 0 {
			return nil, errors.New("calendar color property id not allocated")
		}
		props = append(props, mapi.TaggedPropVal{Tag: colorTag, Value: in.Color})
	}
	if in.Description != nil {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrComment, Value: *in.Description})
	}
	return props, nil
}

// handleUpdateCalendar renames, recolors and describes a calendar (PATCH;
// fields are optional).
// writeCalendarRenameError answers a refused calendar rename: the default
// calendar keeps its name (403), a name a sibling holds conflicts (409), and any
// other failure is a 500.
func writeCalendarRenameError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, objectstore.ErrBuiltinFolder):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this calendar cannot be renamed"})
	case errors.Is(err, objectstore.ErrFolderExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a calendar with that name already exists"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not rename calendar"})
	}
}

func (s *Server) handleUpdateCalendar(w http.ResponseWriter, r *http.Request) {
	var in calendarJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	id := r.PathValue("id")
	fid, ok := calendarOr404(w, st, id)
	if !ok {
		return
	}
	// Resolved before the rename, so a failure leaves the calendar unchanged.
	props, err := calendarProps(st, in)
	if err != nil {
		logError("calendar-props", err, logging.Fields{"folder": fid})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update calendar"})
		return
	}
	if strings.TrimSpace(in.Name) != "" {
		if err := st.SetFolderName(fid, in.Name); err != nil {
			writeCalendarRenameError(w, err)
			return
		}
	}
	if len(props) > 0 {
		if err := st.SetFolderProperties(fid, props); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update calendar"})
			return
		}
	}
	writeJSON(w, http.StatusOK, calendarJSON{ID: id, Name: in.Name, Color: in.Color, IsDefault: id == "calendar", Description: in.Description})
}

// handleDeleteCalendar deletes a calendar and its events. The built-in default
// calendar cannot be deleted.
func (s *Server) handleDeleteCalendar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer closeStore(st)
	fid, ok := calendarOr404(w, st, id)
	if !ok {
		return
	}
	if fid == mapi.PrivateFIDCalendar {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot delete the default calendar"})
		return
	}
	// DeleteFolder moves the calendar's events to Recoverable Items itself.
	if err := st.DeleteFolder(fid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete calendar"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// uidOrGenerated returns the event's UID, minting one when empty so an invite
// always carries a stable meeting identity.
func uidOrGenerated(uid string) string {
	if uid != "" {
		return uid
	}
	return randomHex() + "@hermex"
}

// storeEvent files a new event and returns its message id. organizer names the
// meeting's organizer, empty for an appointment with nobody to meet.
func storeEvent(st *objectstore.Store, e eventJSON, folderID int64, organizer string) (int64, error) {
	if e.UID == "" {
		e.UID = randomHex() + "@hermex"
	}
	msg, err := oxcical.Import(buildICal(e, organizer, 0), oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return 0, err
	}
	id, err := st.CreateMessage(folderID, msg)
	if err != nil {
		return 0, err
	}
	// The event is stored; a busy status that fails to land must not report the
	// create as failed, or a retry stores it twice.
	if err := applyBusyStatus(st, id, e); err != nil {
		st.LogSwallowedError("calendar.busy_status", err)
	}
	// Categories ride the shared PidNameKeywords named prop (the same list every
	// protocol reads), set directly rather than through the iCal CATEGORIES path
	// oxcical's VEVENT does not parse.
	if len(e.Categories) > 0 {
		if err := st.SetCategories(id, e.Categories); err != nil {
			st.LogSwallowedError("calendar.categories", err)
		}
	}
	return id, nil
}
