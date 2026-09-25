package oxcical

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/recurrence"
)

var errNoEvent = errors.New("oxcical: no VEVENT in calendar")

// iCalendar busy/transparency maps to PidLidBusyStatus values.
const (
	busyFree      = mapi.BusyFree
	busyTentative = mapi.BusyTentative
	busyBusy      = mapi.BusyBusy
)

// Import parses an iCalendar object into an IPM.Appointment message. A
// non-recurring event is synthesized into MAPI properties; a recurring event
// (carrying RRULE or RECURRENCE-ID) is preserved verbatim in PrIcalOriginal and
// gets only the minimal listing properties plus, for a series master, the
// MS-OXOCAL AppointmentRecurrencePattern blob Outlook reads in
// PidLidAppointmentRecur. Named properties are resolved through opt.Resolver, a
// floating time is read in opt.DefaultZone, and any time left with no usable zone
// is handed to opt.OnUnresolvedZone before the times are read.
func Import(raw []byte, opt Options) (*oxcmail.Message, error) {
	cal, err := parseICal(raw)
	if err != nil {
		return nil, err
	}
	vev := cal.sub("VEVENT")
	if vev == nil {
		return nil, errNoEvent
	}
	applyDefaultZone(cal, opt.DefaultZone)
	reportZones(cal, opt)

	named, err := namedTags(opt, true)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, true)
	if err != nil {
		return nil, err
	}

	msg := &oxcmail.Message{}
	p := &msg.Props
	class := meetingClass(cal, vev)
	p.Set(mapi.PrMessageClass, class)

	importIdentity(msg, vev, class)
	if uidTag != 0 {
		p.Set(uidTag, importedUID(vev))
	}
	setIf(p, mapi.PrSubject, vev.propText("SUMMARY"))
	importCounterProposal(p, named, cal, vev)

	// Recurring events round-trip verbatim; store only what listing needs.
	if vev.prop("RRULE") != nil || vev.prop("RECURRENCE-ID") != nil {
		importRecurring(p, named, vev, raw)
		return msg, nil
	}

	// Non-recurring: full property synthesis.
	setIf(p, mapi.PrBody, vev.propText("DESCRIPTION"))
	setNamedStr(p, named, mapi.NameAppointmentLocation, vev.propText("LOCATION"))
	importTimes(p, named, vev)
	importClassification(p, named, vev)
	importAlarm(p, named, vev)
	return msg, nil
}

// importIdentity stores the meeting's participants. A meeting's ORGANIZER becomes
// the representing identity (so a response can address its REPLY back) and its
// ATTENDEE list becomes the recipient bags, so the invitee set round-trips through
// the MAPI store for every protocol (single-data) and lets implicit scheduling diff
// who is invited. A plain appointment with neither is left untouched; a response's
// ATTENDEE is the responder (carried via the sender identity), not an invitee, so
// it is not stored as a recipient.
func importIdentity(msg *oxcmail.Message, vev *icomp, class string) {
	atts := vev.propLines("ATTENDEE")
	if class != "IPM.Appointment" || len(atts) > 0 {
		setOrganizer(&msg.Props, vev.prop("ORGANIZER"))
	}
	if !strings.HasPrefix(class, "IPM.Schedule.Meeting.Resp") {
		importAttendees(msg, atts)
	}
}

// importedUID returns the event's stored UID, deriving a stable one when the body
// carries none.
func importedUID(vev *icomp) string {
	if uid := strings.TrimSpace(vev.propText("UID")); uid != "" {
		return uid
	}
	return generatedUID(vev)
}

// importRecurring preserves a recurring event's body verbatim and stores what
// listing needs. A series master (carrying RRULE) is also marked PidLidRecurring
// and gets the MS-OXOCAL AppointmentRecurrencePattern blob Outlook reads in
// PidLidAppointmentRecur; an override (RECURRENCE-ID only) is an exception
// instance and carries neither.
func importRecurring(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, vev *icomp, raw []byte) {
	p.Set(mapi.PrIcalOriginal, append([]byte(nil), raw...))
	var start time.Time
	if l := vev.prop("DTSTART"); l != nil {
		if t, _, ok := parseICalTime(l); ok {
			start = t
			setNamedTime(p, named, mapi.NameAppointmentStartWhole, t)
		}
	}
	loc := eventZone(vev)
	if loc != nil && !start.IsZero() {
		writeDisplayZones(p, named, loc, start)
	}
	rrule := vev.prop("RRULE")
	if rrule == nil {
		return
	}
	// PidLidRecurring marks the series master, which is how a reader finds a
	// series whose first instance lies before the window it asks about.
	setNamedBool(p, named, mapi.NameRecurring, true)
	if start.IsZero() {
		return
	}
	if loc != nil {
		// The pattern's dates and times are read in the zone PidLidTimeZoneStruct
		// names ([MS-OXOCAL] 2.2.1.44.1), so they are computed on that zone's wall
		// clock rather than on UTC.
		start = start.In(loc)
		writeSeriesZone(p, named, loc, start)
	}
	blob, err := recurrence.FromRRule(rrule.value, start)
	if err != nil {
		return
	}
	if tag, ok := named[mapi.NameAppointmentRecur]; ok {
		p.Set(tag, blob)
	}
}

// importTimes stores the event's span, marking an all-day event as such.
func importTimes(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, vev *icomp) {
	l := vev.prop("DTSTART")
	if l == nil {
		return
	}
	start, allDay, ok := parseICalTime(l)
	if !ok {
		return
	}
	setNamedTime(p, named, mapi.NameAppointmentStartWhole, start)
	if end, ok := eventEnd(vev, start, allDay); ok {
		setNamedTime(p, named, mapi.NameAppointmentEndWhole, end)
	}
	if allDay {
		setNamedBool(p, named, mapi.NameAppointmentSubType, true)
		return
	}
	if loc := eventZone(vev); loc != nil {
		writeDisplayZones(p, named, loc, start)
	}
}

// eventZone returns the zone the event's DTSTART names, or nil for a date, a UTC
// or floating value, and a TZID only the stream's own VTIMEZONE describes: the
// Outlook time zone properties need the zone's full rules, which only a named
// zone carries.
func eventZone(vev *icomp) *time.Location {
	l := vev.prop("DTSTART")
	if l == nil || isDateOnly(l, strings.TrimSpace(l.value)) {
		return nil
	}
	return ZoneByID(l.param("TZID"))
}

// zoneKeyName is the KeyName a time zone definition carries: the Windows id
// Outlook matches against its registry, or the IANA name for a zone that has none.
func zoneKeyName(loc *time.Location) string {
	if win, ok := WindowsZoneFor(loc.String()); ok {
		return win
	}
	return loc.String()
}

// writeDisplayZones stores the zone a client shows the start and end in
// ([MS-OXOCAL] 2.2.1.42 and 2.2.1.43).
func writeDisplayZones(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, loc *time.Location, start time.Time) {
	def := tzDefinitionBlob(zoneKeyName(loc), zoneReg(loc, start), tzRuleEffective)
	setNamedValue(p, named, mapi.NameAppointmentTimeZoneDefStartDisplay, def)
	setNamedValue(p, named, mapi.NameAppointmentTimeZoneDefEndDisplay, def)
}

// writeSeriesZone stores the zone a series converts its times by: the legacy
// TZREG with its description ([MS-OXOCAL] 2.2.1.39 and 2.2.1.40) and the
// definition Outlook 2007 and later read ([MS-OXOCAL] 2.2.1.41), kept in step
// by carrying the same rule.
func writeSeriesZone(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, loc *time.Location, start time.Time) {
	reg := zoneReg(loc, start)
	key := zoneKeyName(loc)
	setNamedValue(p, named, mapi.NameTimeZoneStruct, tzStructBlob(reg))
	setNamedValue(p, named, mapi.NameTimeZoneDescription, key)
	setNamedValue(p, named, mapi.NameAppointmentTimeZoneDefRecur, tzDefinitionBlob(key, reg, tzRuleEffective|tzRuleRecurCurrent))
}

// setNamedValue stores a value under a resolved named property, skipping a name
// the store allocated no id for.
func setNamedValue(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, v any) {
	if tag, ok := named[name]; ok {
		p.Set(tag, v)
	}
}

// importClassification stores how the event is filed: whether it takes time, how
// private it is, how urgent, and which revision it is.
func importClassification(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, vev *icomp) {
	setNamedLong(p, named, mapi.NameBusyStatus, busyStatus(vev))
	if c := vev.propText("CLASS"); c != "" {
		p.Set(mapi.PrSensitivity, classSensitivity(c))
	}
	if imp, ok := priorityImportance(vev.propText("PRIORITY")); ok {
		p.Set(mapi.PrImportance, imp)
	}
	// The sequence lands in a 32-bit named long, so parse at that width: an
	// iCalendar body may carry any integer, and a wider one would wrap.
	if n, err := strconv.ParseInt(strings.TrimSpace(vev.propText("SEQUENCE")), 10, 32); err == nil {
		setNamedLong(p, named, mapi.NameAppointmentSequence, int32(n))
	}
}

// importAlarm stores the reminder a VALARM asks for.
func importAlarm(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, vev *icomp) {
	al := vev.sub("VALARM")
	if al == nil {
		return
	}
	mins, ok := alarmMinutes(al)
	if !ok {
		return
	}
	setNamedBool(p, named, mapi.NameReminderSet, true)
	setNamedLong(p, named, mapi.NameReminderDelta, mins)
}

// meetingClass derives the MAPI message class from the iCalendar METHOD (RFC 5546
// iTIP). REQUEST and CANCEL are scheduling messages an attendee acts on; a REPLY
// names the attendee's response in its class suffix (PARTSTAT); a COUNTER is a
// tentative response proposing a new time ([MS-OXCICAL] METHOD table). PUBLISH, an
// absent METHOD, or an unrecognized one is a plain appointment, the prior default.
func meetingClass(cal, vev *icomp) string {
	switch methodOf(cal) {
	case "REQUEST":
		return "IPM.Schedule.Meeting.Request"
	case "CANCEL":
		return "IPM.Schedule.Meeting.Canceled"
	case "COUNTER":
		return "IPM.Schedule.Meeting.Resp.Tent"
	case "REPLY":
		switch strings.ToUpper(strings.TrimSpace(replyPartStat(vev))) {
		case "DECLINED":
			return "IPM.Schedule.Meeting.Resp.Neg"
		case "TENTATIVE":
			return "IPM.Schedule.Meeting.Resp.Tent"
		default: // ACCEPTED or unspecified
			return "IPM.Schedule.Meeting.Resp.Pos"
		}
	}
	return "IPM.Appointment"
}

// methodOf is the calendar's iTIP METHOD, upper-cased.
func methodOf(cal *icomp) string {
	return strings.ToUpper(strings.TrimSpace(cal.propText("METHOD")))
}

// importCounterProposal marks a COUNTER as a counter proposal and stores the span it
// proposes, which its DTSTART and DTEND carry ([MS-OXCICAL] DTSTART and DTEND).
// The organizer's client reads the proposal from these properties.
func importCounterProposal(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, cal, vev *icomp) {
	if methodOf(cal) != "COUNTER" {
		return
	}
	setNamedBool(p, named, mapi.NameAppointmentCounterProposal, true)
	l := vev.prop("DTSTART")
	if l == nil {
		return
	}
	start, allDay, ok := parseICalTime(l)
	if !ok {
		return
	}
	setNamedTime(p, named, mapi.NameAppointmentProposedStartWhole, start)
	if end, ok := eventEnd(vev, start, allDay); ok {
		setNamedTime(p, named, mapi.NameAppointmentProposedEndWhole, end)
	}
}

// replyPartStat returns the PARTSTAT of the reply's attendee line, which names the
// response (ACCEPTED / DECLINED / TENTATIVE) carried by a METHOD:REPLY object.
func replyPartStat(vev *icomp) string {
	if l := vev.prop("ATTENDEE"); l != nil {
		return l.param("PARTSTAT")
	}
	return ""
}

// setOrganizer records an iCalendar ORGANIZER line as the sent-representing
// identity, its mailto address and optional CN, so a meeting response can
// address the organizer. A nil or address-less line is ignored.
func setOrganizer(p *mapi.PropertyValues, l *iline) {
	if l == nil {
		return
	}
	addr := mailtoAddr(l.value)
	if addr == "" {
		return
	}
	p.Set(mapi.PrSentRepresentingSmtpAddress, addr)
	p.Set(mapi.PrSentRepresentingEmailAddress, addr)
	p.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	if cn := l.param("CN"); cn != "" {
		p.Set(mapi.PrSentRepresentingName, cn)
	}
}

// mailtoAddr strips a leading "mailto:" scheme (any case) from a calendar user
// address, leaving the bare SMTP address.
func mailtoAddr(v string) string {
	addr := strings.TrimSpace(v)
	if i := strings.IndexByte(addr, ':'); i >= 0 && strings.EqualFold(addr[:i], "mailto") {
		addr = addr[i+1:]
	}
	return addr
}

// importAttendees appends each ATTENDEE line as a primary (To) recipient bag (its
// SMTP address, address type, and optional CN as the display name), so a meeting's
// invitee set persists as MAPI recipients (MS-OXOCAL §2.2.4.10), visible to every
// protocol and to implicit scheduling. The organizer is recorded separately as the
// representing identity.
func importAttendees(msg *oxcmail.Message, atts []iline) {
	for _, l := range atts {
		addr := mailtoAddr(l.value)
		if addr == "" {
			continue
		}
		rcpt := mapi.PropertyValues{
			{Tag: mapi.PrRecipientType, Value: attendeeRecipientType(l)},
			{Tag: mapi.PrAddrType, Value: "SMTP"},
			{Tag: mapi.PrEmailAddress, Value: addr},
			{Tag: mapi.PrSmtpAddress, Value: addr},
		}
		if cn := l.param("CN"); cn != "" {
			rcpt = append(rcpt, mapi.TaggedPropVal{Tag: mapi.PrDisplayName, Value: cn})
		}
		msg.Recipients = append(msg.Recipients, rcpt)
	}
}

// attendeeRecipientType maps an ATTENDEE's ROLE and CUTYPE parameters to the
// recipient type it is stored under ([MS-OXCICAL] 2.1.3.1.1.20.2, the first
// matching row wins): a required attendee is To, an optional one Cc, and a
// resource, room or non-participant Bcc.
func attendeeRecipientType(l iline) int32 {
	role := strings.ToUpper(l.param("ROLE"))
	switch role {
	case "CHAIR", "REQ-PARTICIPANT":
		return mapi.RecipTo
	case "OPT-PARTICIPANT":
		return mapi.RecipCc
	}
	switch strings.ToUpper(l.param("CUTYPE")) {
	case "RESOURCE", "ROOM":
		return mapi.RecipBcc
	}
	if role == "NON-PARTICIPANT" {
		return mapi.RecipBcc
	}
	return mapi.RecipTo
}

// eventEnd resolves the event end from DTEND, else DTSTART+DURATION, else (for an
// all-day event) one day after the start, else the start itself (zero length).
func eventEnd(vev *icomp, start time.Time, allDay bool) (time.Time, bool) {
	if l := vev.prop("DTEND"); l != nil {
		if t, _, ok := parseICalTime(l); ok {
			return t, true
		}
	}
	if d, ok := parseICalDuration(vev.propText("DURATION")); ok {
		return start.Add(d), true
	}
	if allDay {
		return start.Add(24 * time.Hour), true
	}
	return start, true
}

// busyStatus derives PidLidBusyStatus from TRANSP (transparent ⇒ free) and STATUS
// (tentative ⇒ tentative), defaulting to busy.
func busyStatus(vev *icomp) int32 {
	if strings.EqualFold(strings.TrimSpace(vev.propText("TRANSP")), "TRANSPARENT") {
		return busyFree
	}
	if strings.EqualFold(strings.TrimSpace(vev.propText("STATUS")), "TENTATIVE") {
		return busyTentative
	}
	return busyBusy
}

// classSensitivity maps iCalendar CLASS to PR_SENSITIVITY (PUBLIC ⇒ none).
func classSensitivity(c string) int32 {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "PRIVATE":
		return mapi.SensitivityPrivate
	case "CONFIDENTIAL":
		return mapi.SensitivityConfidential
	}
	return mapi.SensitivityNone
}

// priorityImportance maps an iCalendar PRIORITY (1-9) to PR_IMPORTANCE; 1-4 high,
// 5 normal, 6-9 low. ok is false for an absent or 0 (undefined) priority.
func priorityImportance(s string) (int32, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	switch {
	case n >= 1 && n <= 4:
		return mapi.ImportanceHigh, true
	case n == 5:
		return mapi.ImportanceNormal, true
	case n >= 6 && n <= 9:
		return mapi.ImportanceLow, true
	}
	return 0, false
}

// alarmMinutes returns the reminder lead time in minutes from a VALARM TRIGGER
// duration (a "-PT15M" trigger is 15 minutes before the start).
func alarmMinutes(al *icomp) (int32, bool) {
	l := al.prop("TRIGGER")
	if l == nil {
		return 0, false
	}
	d, ok := parseICalDuration(l.value)
	if !ok {
		return 0, false
	}
	// #nosec G115 -- a Duration is int64 nanoseconds, so its minute count never leaves the 32-bit range
	mins := int32(-d / time.Minute)
	if mins < 0 {
		mins = -mins
	}
	return mins, true
}

// generatedUID derives a deterministic UID for an event that carries none, so the
// same input yields the same identity. It is based on the summary.
func generatedUID(vev *icomp) string {
	base := vev.propText("SUMMARY")
	if base == "" {
		base = "event"
	}
	return "hermex-" + strings.Map(func(r rune) rune {
		if r == ' ' {
			return '-'
		}
		return r
	}, strings.ToLower(strings.TrimSpace(base)))
}

// setIf sets a string property only when the value is non-empty.
func setIf(p *mapi.PropertyValues, tag mapi.PropTag, v string) {
	if v != "" {
		p.Set(tag, v)
	}
}

// setNamedStr sets a named string property when its tag resolved and v is non-empty.
func setNamedStr(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, v string) {
	if v == "" {
		return
	}
	if tag, ok := named[name]; ok {
		p.Set(tag, v)
	}
}

// setNamedTime sets a named PtSysTime property as a UTC FILETIME.
func setNamedTime(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, t time.Time) {
	if tag, ok := named[name]; ok {
		p.Set(tag, mapi.UnixToNTTime(t.UTC()))
	}
}

// setNamedBool sets a named PtBoolean property.
func setNamedBool(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, b bool) {
	if tag, ok := named[name]; ok {
		p.Set(tag, b)
	}
}

// setNamedLong sets a named PtLong property.
func setNamedLong(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, n int32) {
	if tag, ok := named[name]; ok {
		p.Set(tag, n)
	}
}
