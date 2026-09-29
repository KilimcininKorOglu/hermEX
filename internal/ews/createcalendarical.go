package ews

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"hermex/internal/oxcical"
)

// recurrenceRequest is a <t:Recurrence> ([MS-OXWSCDATA] RecurrenceType): one
// pattern and one range.
type recurrenceRequest struct {
	Daily           *recurrencePattern `xml:"DailyRecurrence"`
	Weekly          *recurrencePattern `xml:"WeeklyRecurrence"`
	AbsoluteMonthly *recurrencePattern `xml:"AbsoluteMonthlyRecurrence"`
	RelativeMonthly *recurrencePattern `xml:"RelativeMonthlyRecurrence"`
	AbsoluteYearly  *recurrencePattern `xml:"AbsoluteYearlyRecurrence"`
	RelativeYearly  *recurrencePattern `xml:"RelativeYearlyRecurrence"`
	EndDate         *struct {
		EndDate string `xml:"EndDate"`
	} `xml:"EndDateRecurrence"`
	Numbered *struct {
		Count int `xml:"NumberOfOccurrences"`
	} `xml:"NumberedRecurrence"`
}

// recurrencePattern carries the fields of every pattern type; each type reads the
// ones it defines.
type recurrencePattern struct {
	Interval       int    `xml:"Interval"`
	DaysOfWeek     string `xml:"DaysOfWeek"`
	DayOfWeekIndex string `xml:"DayOfWeekIndex"`
	DayOfMonth     int    `xml:"DayOfMonth"`
	Month          string `xml:"Month"`
}

// icalDays maps a DayOfWeekType value to its iCalendar BYDAY list.
var icalDays = map[string]string{
	"Sunday": "SU", "Monday": "MO", "Tuesday": "TU", "Wednesday": "WE",
	"Thursday": "TH", "Friday": "FR", "Saturday": "SA",
	"Day": "SU,MO,TU,WE,TH,FR,SA", "Weekday": "MO,TU,WE,TH,FR", "WeekendDay": "SA,SU",
}

// weekIndexes maps a DayOfWeekIndexType value to its BYSETPOS.
var weekIndexes = map[string]string{"First": "1", "Second": "2", "Third": "3", "Fourth": "4", "Last": "-1"}

// months maps a MonthNamesType value to its BYMONTH.
var months = map[string]int{
	"January": 1, "February": 2, "March": 3, "April": 4, "May": 5, "June": 6,
	"July": 7, "August": 8, "September": 9, "October": 10, "November": 11, "December": 12,
}

// iCalendar renders the item as the VEVENT the store imports. organizer names the
// meeting's organizer, empty for an appointment.
func (item createCalendarItem) iCalendar(organizer string) ([]byte, string) {
	if err := item.checkFields(); err != nil {
		return nil, "ErrorInvalidPropertySet"
	}
	loc := item.zone()
	start, ok1 := parseEWSTime(item.Start, loc)
	end, ok2 := parseEWSTime(item.End, loc)
	if !ok1 || !ok2 {
		return nil, "ErrorInvalidRequest"
	}
	if end.Before(start) {
		return nil, "ErrorCalendarEndDateIsEarlierThanStartDate"
	}
	rrule, ok := item.Recurrence.rrule(item.IsAllDayEvent, loc)
	if !ok {
		return nil, "ErrorCalendarInvalidRecurrence"
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//EWS//EN\r\n")
	if loc != time.UTC && !item.IsAllDayEvent {
		for _, line := range oxcical.VTimezone(loc, start.In(loc).Year()) {
			b.WriteString(line)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("BEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\nDTSTAMP:%s\r\n", icalEscape(item.uid()), time.Now().UTC().Format("20060102T150405Z"))
	fmt.Fprintf(&b, "DTSTART%s\r\nDTEND%s\r\n", icalWhen(start, item.IsAllDayEvent, loc), icalWhen(end, item.IsAllDayEvent, loc))
	if rrule != "" {
		fmt.Fprintf(&b, "RRULE:%s\r\n", rrule)
	}
	item.writeDetails(&b, organizer)
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return []byte(b.String()), ""
}

// writeDetails writes the text, people, reminder and class of the event.
func (item createCalendarItem) writeDetails(b *strings.Builder, organizer string) {
	fmt.Fprintf(b, "SUMMARY:%s\r\n", icalEscape(item.Subject))
	if item.Location != "" {
		fmt.Fprintf(b, "LOCATION:%s\r\n", icalEscape(item.Location))
	}
	if item.Body.Content != "" && !strings.EqualFold(item.Body.Type, "HTML") {
		fmt.Fprintf(b, "DESCRIPTION:%s\r\n", icalEscape(item.Body.Content))
	}
	if organizer != "" {
		fmt.Fprintf(b, "ORGANIZER:mailto:%s\r\n", organizer)
	}
	item.writeAttendees(b)
	if minutes, ok := item.reminder(); ok {
		fmt.Fprintf(b, "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT%dM\r\nEND:VALARM\r\n", minutes)
	}
	switch item.Sensitivity {
	case "Personal", "Private":
		b.WriteString("CLASS:PRIVATE\r\n")
	case "Confidential":
		b.WriteString("CLASS:CONFIDENTIAL\r\n")
	}
}

// writeAttendees writes one ATTENDEE line per attendee, each once, in the role its
// list gives it: a required or optional participant, or a resource.
func (item createCalendarItem) writeAttendees(b *strings.Builder) {
	seen := map[string]bool{}
	for _, list := range []struct {
		refs  attendeeRefs
		param string
	}{
		{item.RequiredAttendees, "ROLE=REQ-PARTICIPANT;RSVP=TRUE"},
		{item.OptionalAttendees, "ROLE=OPT-PARTICIPANT;RSVP=TRUE"},
		{item.Resources, "CUTYPE=RESOURCE;ROLE=NON-PARTICIPANT"},
	} {
		for _, a := range list.refs.Attendee {
			addr, ok := cleanAddress(a.Mailbox.EmailAddress)
			if !ok || seen[addr] {
				continue
			}
			seen[addr] = true
			fmt.Fprintf(b, "ATTENDEE;CN=\"%s\";%s:mailto:%s\r\n", paramText(a.Mailbox.Name, addr), list.param, addr)
		}
	}
}

// reminder is the reminder lead time, ok false when the item sets none. An item
// that says nothing about a reminder gets Exchange's default.
func (item createCalendarItem) reminder() (int, bool) {
	if item.ReminderIsSet != nil && !*item.ReminderIsSet {
		return 0, false
	}
	if item.ReminderMinutes != nil && *item.ReminderMinutes >= 0 {
		return *item.ReminderMinutes, true
	}
	return defaultReminderMinutes, true
}

// uid is the iCalendar UID the item is stored under.
func (item createCalendarItem) uid() string {
	if item.UID != "" {
		return item.UID
	}
	return newCalendarUID()
}

// zone is the time zone the item's wall clock is kept in: the one the request
// names, else UTC.
func (item createCalendarItem) zone() *time.Location {
	if item.StartTimeZone != nil {
		if loc := oxcical.ZoneByID(item.StartTimeZone.ID); loc != nil {
			return loc
		}
	}
	if item.MeetingTimeZone != nil {
		if loc := oxcical.ZoneByID(item.MeetingTimeZone.Name); loc != nil {
			return loc
		}
	}
	return time.UTC
}

// parseEWSTime reads an xs:dateTime; one without an offset is a wall clock in loc.
func parseEWSTime(v string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", v, loc)
	return t, err == nil
}

// icalWhen renders a DTSTART or DTEND value with its parameters.
func icalWhen(t time.Time, allDay bool, loc *time.Location) string {
	switch {
	case allDay:
		return ";VALUE=DATE:" + t.In(loc).Format("20060102")
	case loc == time.UTC:
		return ":" + t.UTC().Format("20060102T150405Z")
	}
	return ";TZID=" + loc.String() + ":" + t.In(loc).Format("20060102T150405")
}

// icalEscape escapes a TEXT value (RFC 5545 3.3.11), so a value cannot end its
// content line and inject another property.
func icalEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\r\n", `\n`, "\n", `\n`, "\r", "")
	return r.Replace(s)
}

// paramText makes a display name safe inside a quoted parameter value, which
// admits no DQUOTE and no control character (RFC 5545 3.1); an empty name reads
// as the address.
func paramText(name, addr string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '"' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if clean == "" {
		return addr
	}
	return clean
}

// rrule renders the recurrence as an RRULE value, "" for none; ok is false for a
// pattern or range that does not describe a recurrence.
func (r *recurrenceRequest) rrule(allDay bool, loc *time.Location) (string, bool) {
	if r == nil {
		return "", true
	}
	rule, ok := r.pattern()
	if !ok {
		return "", false
	}
	limit, ok := r.limit(allDay, loc)
	return rule + limit, ok
}

// pattern renders the recurrence's pattern.
func (r *recurrenceRequest) pattern() (string, bool) {
	switch {
	case r.Daily != nil:
		return "FREQ=DAILY" + interval(r.Daily.Interval), true
	case r.Weekly != nil:
		days, ok := weekDays(r.Weekly.DaysOfWeek)
		return "FREQ=WEEKLY" + interval(r.Weekly.Interval) + ";BYDAY=" + days, ok
	case r.AbsoluteMonthly != nil:
		p := r.AbsoluteMonthly
		return fmt.Sprintf("FREQ=MONTHLY%s;BYMONTHDAY=%d", interval(p.Interval), p.DayOfMonth), validMonthDay(p.DayOfMonth)
	case r.RelativeMonthly != nil:
		return relative("FREQ=MONTHLY"+interval(r.RelativeMonthly.Interval), r.RelativeMonthly)
	case r.AbsoluteYearly != nil:
		return absoluteYearly(r.AbsoluteYearly)
	case r.RelativeYearly != nil:
		m, ok := months[r.RelativeYearly.Month]
		if !ok {
			return "", false
		}
		return relative("FREQ=YEARLY;BYMONTH="+strconv.Itoa(m), r.RelativeYearly)
	}
	return "", false
}

// limit renders the recurrence's range: a count, an end date, or none.
func (r *recurrenceRequest) limit(allDay bool, loc *time.Location) (string, bool) {
	switch {
	case r.Numbered != nil:
		return ";COUNT=" + strconv.Itoa(r.Numbered.Count), r.Numbered.Count > 0
	case r.EndDate != nil:
		v := r.EndDate.EndDate
		if len(v) < 10 {
			return "", false
		}
		d, err := time.ParseInLocation("2006-01-02", v[:10], loc)
		if err != nil {
			return "", false
		}
		if allDay {
			return ";UNTIL=" + d.Format("20060102"), true
		}
		return ";UNTIL=" + d.Add(24*time.Hour-time.Second).UTC().Format("20060102T150405Z"), true
	}
	return "", true
}

// weekDays renders a space-separated list of day names as a BYDAY list.
func weekDays(list string) (string, bool) {
	var out []string
	for d := range strings.FieldsSeq(list) {
		v, ok := icalDays[d]
		if !ok {
			return "", false
		}
		out = append(out, v)
	}
	return strings.Join(out, ","), len(out) > 0
}

// relative renders a relative pattern: the days and which of them in the period.
func relative(prefix string, p *recurrencePattern) (string, bool) {
	days, ok1 := weekDays(p.DaysOfWeek)
	pos, ok2 := weekIndexes[p.DayOfWeekIndex]
	return prefix + ";BYDAY=" + days + ";BYSETPOS=" + pos, ok1 && ok2
}

// absoluteYearly renders a yearly pattern on one date.
func absoluteYearly(p *recurrencePattern) (string, bool) {
	m, ok := months[p.Month]
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYMONTHDAY=%d", m, p.DayOfMonth), ok && validMonthDay(p.DayOfMonth)
}

// interval renders an INTERVAL part, empty for the default of one.
func interval(n int) string {
	if n > 1 {
		return ";INTERVAL=" + strconv.Itoa(n)
	}
	return ""
}

// validMonthDay reports whether a day of the month exists in some month.
func validMonthDay(d int) bool {
	return d >= 1 && d <= 31
}
