package webmail2api

import (
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// decodeEvent reads an event create or update body, answering 400 and reporting
// false when it does not decode or carries a recurrence rule that is not one.
func decodeEvent(w http.ResponseWriter, r *http.Request) (eventJSON, bool) {
	var in eventJSON
	if err := decodeJSON(r, &in); err != nil || (in.Recurrence != "" && !validRRule(in.Recurrence)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return eventJSON{}, false
	}
	return in, true
}

// validRRule reports whether s is an RRULE value that can be written on its own
// content line: it parses, and it holds only the characters a rule is spelled in,
// so no line break or parameter can ride along into the stored calendar.
func validRRule(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !isRRuleChar(c) {
			return false
		}
	}
	_, ok := oxcical.ParseRRule(s)
	return ok
}

// isRRuleChar reports whether c belongs to the RRULE value alphabet.
func isRRuleChar(c rune) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.ContainsRune("=;,+-", c)
}

// eventLocation is the zone a timed event's wall clock is written in, or nil for
// an all-day event and for a zone name that does not resolve, both written as
// they are today.
func eventLocation(e eventJSON) *time.Location {
	if e.AllDay {
		return nil
	}
	return oxcical.ZoneByID(e.Timezone)
}

// writeVTimezone writes the VTIMEZONE a TZID in the event needs (RFC 5545
// section 3.6.5), describing the zone in the year the event starts.
func writeVTimezone(b *strings.Builder, loc *time.Location, start string) {
	if loc == nil {
		return
	}
	t, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return
	}
	for _, line := range oxcical.VTimezone(loc, t.In(loc).Year()) {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
}

// icalTimeIn renders a DTSTART or DTEND value after its property name: the wall
// clock in loc with its TZID when there is a zone, else toICalTime's UTC or date
// form. loc.String() is a tzdata name, so it cannot carry a parameter delimiter.
func icalTimeIn(v string, allDay bool, loc *time.Location) string {
	if loc == nil {
		return toICalTime(v, allDay)
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return toICalTime(v, allDay)
	}
	return ";TZID=" + loc.String() + ":" + t.In(loc).Format("20060102T150405")
}

// icalSpan fills the event's start and end from DTSTART and DTEND, reading a
// value with a TZID in that zone and reporting the zone as the event's timezone.
func icalSpan(ics []byte, e *eventJSON) {
	if v, p := icalProp(ics, "DTSTART"); v != "" {
		var zone string
		e.Start, e.AllDay, zone = fromICalTimeIn(v, p)
		if zone != "" {
			e.Timezone = zone
		}
	}
	if v, p := icalProp(ics, "DTEND"); v != "" {
		e.End, _, _ = fromICalTimeIn(v, p)
	}
}

// fromICalTimeIn is fromICalTime for a value whose parameters may name a zone: a
// wall-clock value with a resolvable TZID is read in that zone and returned as a
// UTC instant together with the zone's IANA name.
func fromICalTimeIn(v, params string) (string, bool, string) {
	loc := oxcical.ZoneByID(icalParam(params, "TZID"))
	v = strings.TrimSpace(v)
	if loc == nil || strings.HasSuffix(v, "Z") {
		s, allDay := fromICalTime(v)
		return s, allDay, ""
	}
	t, err := time.ParseInLocation("20060102T150405", v, loc)
	if err != nil {
		s, allDay := fromICalTime(v)
		return s, allDay, ""
	}
	return t.UTC().Format(time.RFC3339), false, loc.String()
}

// icalParam returns one parameter's value from a property key's parameter part
// (";TZID=Europe/Berlin;VALUE=DATE-TIME"), with any quotes removed.
func icalParam(params, name string) string {
	for part := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// expandedRows turns a series into one row per instance in [start, end), each
// carrying the instance's span and generated instant and the series' own first
// span. A single event is returned as it is.
func expandedRows(e eventJSON, ics []byte, start, end time.Time) []eventJSON {
	insts, ok := oxcical.InstancesIn(ics, start, end)
	if !ok {
		return []eventJSON{e}
	}
	out := make([]eventJSON, 0, len(insts))
	for _, in := range insts {
		row := e
		row.SeriesStart, row.SeriesEnd = e.Start, e.End
		row.Start, row.End = spanText(in.Start, e.AllDay), spanText(in.End, e.AllDay)
		row.Occurrence = in.At.UTC().Format(time.RFC3339)
		out = append(out, row)
	}
	return out
}

// spanText renders an instance boundary the way the event's own start is
// written: a date for an all-day event, a UTC RFC 3339 instant otherwise.
func spanText(t time.Time, allDay bool) string {
	if allDay {
		return t.UTC().Format("2006-01-02")
	}
	return t.UTC().Format(time.RFC3339)
}

// mergeFolderObjects appends the objects of b that a does not already hold.
func mergeFolderObjects(a, b []objectstore.FolderObject) []objectstore.FolderObject {
	seen := make(map[int64]bool, len(a))
	for _, o := range a {
		seen[o.ID] = true
	}
	for _, o := range b {
		if !seen[o.ID] {
			seen[o.ID] = true
			a = append(a, o)
		}
	}
	return a
}
