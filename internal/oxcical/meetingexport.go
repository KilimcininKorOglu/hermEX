package oxcical

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/recurrence"
)

// exportOnlyNamed are the named properties a MAPI client (Outlook) writes on a
// meeting and only Export reads. Import never writes them, so they stay out of
// appointmentNamed and out of the managed set an edit clears.
var exportOnlyNamed = []namedField{
	{mapi.NameGlobalObjectId, mapi.PtBinary},
	{mapi.NameExceptionReplaceTime, mapi.PtSysTime},
}

// exportFields is every named property Export reads.
var exportFields = slices.Concat(appointmentNamed, exportOnlyNamed)

// Layout of PidLidGlobalObjectId ([MS-OXOCAL] 2.2.1.27): a 16-byte class id, the
// instance date (year high byte, year low byte, month, day), an 8-byte creation
// time, 8 reserved bytes, the 4-byte data size and the data.
const (
	goidDateOffset = 16
	goidSizeOffset = 36
	goidDataOffset = 40
)

// vcalUIDMarker opens the data of a global object id that wraps a UID another
// calendar system chose ([MS-OXCICAL] UID).
var vcalUIDMarker = []byte("vCal-Uid\x01\x00\x00\x00")

// globalObjectUID renders a global object id as the iCalendar UID [MS-OXCICAL]
// exports for it: a wrapped foreign UID as itself, anything else as the upper-case
// hex of the id with its instance date cleared, so every instance of a meeting
// shares the UID of the series. It returns "" for a value too short to be an id.
func globalObjectUID(goid []byte) string {
	if len(goid) < goidDataOffset {
		return ""
	}
	data := goid[goidDataOffset:]
	if size := int(binary.LittleEndian.Uint32(goid[goidSizeOffset:])); size <= len(data) {
		data = data[:size]
	}
	if rest, ok := bytes.CutPrefix(data, vcalUIDMarker); ok {
		return string(rest)
	}
	clean := bytes.Clone(goid[:goidDataOffset+len(data)])
	clear(clean[goidDateOffset : goidDateOffset+4])
	return strings.ToUpper(hex.EncodeToString(clean))
}

// goidInstanceDate reads the instance date a global object id carries for a
// message about one occurrence. ok is false for the id of a whole meeting, whose
// date bytes are zero.
func goidInstanceDate(goid []byte) (year int, month time.Month, day int, ok bool) {
	if len(goid) < goidDataOffset {
		return 0, 0, 0, false
	}
	year = int(goid[goidDateOffset])<<8 | int(goid[goidDateOffset+1])
	month, day = time.Month(goid[goidDateOffset+2]), int(goid[goidDateOffset+3])
	if year == 0 || month < time.January || month > time.December || day < 1 || day > 31 {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

// eventExport is one object being rendered: what it is (a plain appointment, a
// series, one instance, or a meeting message with its iTIP method) and the zone its
// wall clocks belong to.
type eventExport struct {
	msg      *oxcmail.Message
	p        *mapi.PropertyValues
	named    map[mapi.PropertyName]mapi.PropTag
	uid      string
	partstat string // the responder's PARTSTAT, for a response
	method   string // the iTIP METHOD, "" for a plain calendar object
	counter  bool   // a response proposing a new time
	allDay   bool
	start    time.Time
	hasStart bool
	zone     *time.Location // the named zone the object's wall clocks belong to, or nil
	instance time.Time      // the RECURRENCE-ID of a message about one occurrence
	series   *recurrence.AppointmentPattern
	rrule    string
	zoned    bool // times are written as wall clocks with a TZID and a VTIMEZONE
}

// newEventExport reads what the object is. A tentative response flagged as a
// counter proposal is a COUNTER carrying the proposed span ([MS-OXCICAL] METHOD
// table, DTSTART, DTEND).
func newEventExport(msg *oxcmail.Message, named map[mapi.PropertyName]mapi.PropTag, uidTag mapi.PropTag, class string) *eventExport {
	p := &msg.Props
	e := &eventExport{msg: msg, p: p, named: named, partstat: responsePartStat(class), method: classMethod(class)}
	if e.partstat == "TENTATIVE" && namedBool(p, named, mapi.NameAppointmentCounterProposal) {
		e.method, e.counter = "COUNTER", true
	}
	e.uid = eventUID(p, named, uidTag)
	e.allDay = namedBool(p, named, mapi.NameAppointmentSubType)
	e.start, e.hasStart = namedTime(p, named, mapi.NameAppointmentStartWhole)
	e.zone = storedZone(p, named)
	e.findInstance()
	e.findSeries()
	e.zoned = e.zone != nil && !e.allDay && (e.series != nil || !e.instance.IsZero())
	return e
}

// storedZone is the named zone an object's wall clocks belong to: the zone its
// series definition names, else the zone its start is shown in, else its time zone
// description when that is a zone id. It is nil when none resolves.
func storedZone(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag) *time.Location {
	for _, name := range []mapi.PropertyName{mapi.NameAppointmentTimeZoneDefRecur, mapi.NameAppointmentTimeZoneDefStartDisplay} {
		if loc := ZoneByID(tzDefinitionKeyName(namedBytes(p, named, name))); loc != nil {
			return loc
		}
	}
	return ZoneByID(namedStr(p, named, mapi.NameTimeZoneDescription))
}

// findInstance reads which occurrence a message about one occurrence is about
// ([MS-OXCICAL] RECURRENCE-ID): the replace time when stored, else the instance
// date of the global object id at the wall-clock time the message starts, which is
// what Outlook's own export writes for an instance cancellation.
func (e *eventExport) findInstance() {
	if t, ok := namedTime(e.p, e.named, mapi.NameExceptionReplaceTime); ok {
		e.instance = t
		return
	}
	y, m, d, ok := goidInstanceDate(namedBytes(e.p, e.named, mapi.NameGlobalObjectId))
	if !ok || !e.hasStart {
		return
	}
	w := e.start.In(e.wallZone())
	e.instance = time.Date(y, m, d, w.Hour(), w.Minute(), w.Second(), 0, w.Location())
}

// findSeries decodes the recurrence a series master carries in PidLidAppointmentRecur
// when the object is exported with its recurrence: a plain appointment or a meeting
// request. A cancellation or a response names the meeting by UID alone, so it
// carries none. A pattern this package cannot render leaves the object a single
// event.
func (e *eventExport) findSeries() {
	if !e.instance.IsZero() || !e.hasStart || (e.method != "" && e.method != "REQUEST") {
		return
	}
	blob := namedBytes(e.p, e.named, mapi.NameAppointmentRecur)
	if len(blob) == 0 {
		return
	}
	pat, err := recurrence.DecodeAppointment(blob)
	if err != nil {
		return
	}
	e.series = &pat
	var until time.Time
	if pat.EndType == recurrence.EndAfterDate {
		until = e.occurrence(pat.EndDate).UTC()
	}
	rule, ok := pat.RRuleUntil(until)
	if !ok {
		e.series = nil
		return
	}
	e.rrule = rule
}

// wallZone is the zone the stored wall clocks are read in: the named zone, else the
// fixed offset that puts a series start at the time of day its pattern records,
// else UTC.
func (e *eventExport) wallZone() *time.Location {
	if e.zone != nil {
		return e.zone
	}
	if e.series != nil && e.series.HasTimes && e.hasStart {
		return fixedZoneOf(e.start, e.series.StartTimeOffset)
	}
	return time.UTC
}

// fixedZoneOf is the UTC offset at which instant falls on the wall-clock minute of
// day minuteOfDay, folded into the fourteen hours a zone can be from UTC.
func fixedZoneOf(instant time.Time, minuteOfDay uint32) *time.Location {
	u := instant.UTC()
	diff := int(minuteOfDay) - (u.Hour()*60 + u.Minute())
	switch {
	case diff > 14*60:
		diff -= 24 * 60
	case diff < -14*60:
		diff += 24 * 60
	}
	return time.FixedZone("", diff*60)
}

// occurrence is the start of the series occurrence on the day the pattern records
// as minutes since 1601: that day at the time of day every occurrence starts.
func (e *eventExport) occurrence(day uint32) time.Time {
	loc := e.wallZone()
	minutes := e.series.StartTimeOffset
	if !e.series.HasTimes {
		w := e.start.In(loc)
		minutes = uint32(w.Hour()*60 + w.Minute()) // #nosec G115 -- a minute of the day
	}
	return recurrence.WallClock(day+minutes, loc)
}

// timeLine renders a DTSTART-shaped line: a date for an all-day event, a wall clock
// under the zone's TZID for a zoned object, else a UTC value.
func (e *eventExport) timeLine(name string, t time.Time) string {
	switch {
	case e.allDay:
		return name + ";VALUE=DATE:" + t.In(e.wallZone()).Format("20060102")
	case e.zoned:
		return name + ";TZID=" + e.zone.String() + ":" + t.In(e.zone).Format("20060102T150405")
	}
	return name + ":" + formatICalUTC(t)
}

// verbatimMeeting returns a whole-meeting request or cancellation whose iCalendar
// was preserved verbatim, with the message's METHOD. A message about one occurrence
// is always synthesized, because a preserved body describes the whole series and
// cancelling with it would cancel every occurrence.
func (e *eventExport) verbatimMeeting() ([]byte, bool) {
	if (e.method != "REQUEST" && e.method != "CANCEL") || !e.instance.IsZero() {
		return nil, false
	}
	raw, ok := storedICal(e.p)
	if !ok {
		return nil, false
	}
	return WithMethod(raw, e.method)
}

// render synthesizes the object: the calendar head, the VTIMEZONE a zoned object
// names, the event, and the modified occurrences of a series.
func (e *eventExport) render() []byte {
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	b.add("VERSION:2.0")
	b.add("PRODID:-//hermEX//CalDAV//EN")
	if e.method != "" {
		b.add("METHOD:" + e.method)
	}
	if e.zoned {
		anchor := e.instance
		if e.hasStart {
			anchor = e.start
		}
		for _, l := range VTimezone(e.zone, anchor.In(e.zone).Year()) {
			b.add(l)
		}
	}
	e.writeEvent(b)
	e.writeOverrides(b)
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}

// writeEvent emits the object's own VEVENT.
func (e *eventExport) writeEvent(b *builder) {
	b.add("BEGIN:VEVENT")
	b.line("UID", e.uid)
	e.writeSchedule(b)
	e.writeRecurrence(b)
	if e.method == "CANCEL" {
		b.add("STATUS:CANCELLED")
	}
	exportClassification(b, e.p, e.named)
	exportAlarm(b, e.p, e.named)
	exportIdentity(b, e.msg, e.partstat)
	b.add("END:VEVENT")
}

// writeSchedule emits the event's stamp, text and time span. DTSTAMP is required
// (RFC 5545 §3.8.7.2); the start is a stable, deterministic stamp for a synthesized
// event. A counter proposal's span is the one it proposes.
func (e *eventExport) writeSchedule(b *builder) {
	startName, endName := mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole
	if e.counter {
		startName, endName = mapi.NameAppointmentProposedStartWhole, mapi.NameAppointmentProposedEndWhole
	}
	start, hasStart := namedTime(e.p, e.named, startName)
	end, hasEnd := namedTime(e.p, e.named, endName)

	if hasStart {
		b.add("DTSTAMP:" + formatICalUTC(start))
	}
	addLine(b, "SUMMARY", getStr(e.p, mapi.PrSubject))
	addLine(b, "DESCRIPTION", getStr(e.p, mapi.PrBody))
	addLine(b, "LOCATION", namedStr(e.p, e.named, mapi.NameAppointmentLocation))
	if hasStart {
		b.add(e.timeLine("DTSTART", start))
	}
	if hasEnd {
		b.add(e.timeLine("DTEND", end))
	}
}

// writeRecurrence emits the RECURRENCE-ID of a message about one occurrence, or the
// RRULE and EXDATE of a series.
//
// DeletedInstanceDates lists the original day of every deleted and every modified
// occurrence. A modified one is its own VEVENT (writeOverrides), and an EXDATE on
// its original start would remove the instance that override replaces: RFC 5545
// §3.8.5.1 takes an excluded start out of the set, and this package's own expansion
// then drops the override with it. So only the days no exception came from are
// excluded. When the exceptions could not be read, every listed day is excluded,
// because a moved occurrence shown at its old time is worse than one not shown.
func (e *eventExport) writeRecurrence(b *builder) {
	if !e.instance.IsZero() {
		b.add(e.timeLine("RECURRENCE-ID", e.instance))
		return
	}
	if e.series == nil {
		return
	}
	b.add("RRULE:" + e.rrule)
	modified := map[uint32]bool{}
	for _, ex := range e.series.Exceptions {
		modified[ex.OriginalStart-ex.OriginalStart%minutesPerDay] = true
	}
	for _, day := range e.series.DeletedDates {
		if !modified[day] {
			b.add(e.timeLine("EXDATE", e.occurrence(day)))
		}
	}
}

// minutesPerDay is the length of the pattern's day unit.
const minutesPerDay = 24 * 60

// writeOverrides emits one VEVENT per modified occurrence of a series, from its
// ExceptionInfo as [MS-OXCICAL] RECURRENCE-ID specifies for an exception exported
// without its attachment. A cancellation cancels the whole series and carries none.
func (e *eventExport) writeOverrides(b *builder) {
	if e.series == nil || e.method == "CANCEL" {
		return
	}
	for i := range e.series.Exceptions {
		e.writeOverride(b, &e.series.Exceptions[i])
	}
}

// writeOverride emits one modified occurrence: its original start as the
// RECURRENCE-ID, its own span, and the subject, location and busy status it changes
// or the series' own.
func (e *eventExport) writeOverride(b *builder, ex *recurrence.Exception) {
	loc := e.wallZone()
	b.add("BEGIN:VEVENT")
	b.line("UID", e.uid)
	b.add("DTSTAMP:" + formatICalUTC(e.start))
	b.add(e.timeLine("RECURRENCE-ID", recurrence.WallClock(ex.OriginalStart, loc)))
	addLine(b, "SUMMARY", overridden(ex, recurrence.OverrideSubject, ex.Subject, getStr(e.p, mapi.PrSubject)))
	addLine(b, "LOCATION", overridden(ex, recurrence.OverrideLocation, ex.Location, namedStr(e.p, e.named, mapi.NameAppointmentLocation)))
	b.add(e.timeLine("DTSTART", recurrence.WallClock(ex.Start, loc)))
	b.add(e.timeLine("DTEND", recurrence.WallClock(ex.End, loc)))
	if ex.Flags&recurrence.OverrideBusyStatus != 0 {
		b.add(transpLine(ex.BusyStatus))
	} else if busy, ok := namedLong(e.p, e.named, mapi.NameBusyStatus); ok {
		b.add(transpLine(busy))
	}
	if seq, ok := namedLong(e.p, e.named, mapi.NameAppointmentSequence); ok {
		b.line("SEQUENCE", strconv.Itoa(int(seq)))
	}
	exportIdentity(b, e.msg, e.partstat)
	b.add("END:VEVENT")
}

// overridden returns an exception's own value when its flag says it changed it,
// else the series value.
func overridden(ex *recurrence.Exception, flag uint16, own, series string) string {
	if ex.Flags&flag != 0 {
		return own
	}
	return series
}

// namedBytes returns a named PtBinary property's value (nil when absent).
func namedBytes(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName) []byte {
	if tag, ok := named[name]; ok {
		if v, ok := p.Get(tag); ok {
			if b, ok := v.([]byte); ok {
				return b
			}
		}
	}
	return nil
}
