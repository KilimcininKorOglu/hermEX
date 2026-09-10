package oxcical

import (
	"strings"
	"time"
)

// A series and its exceptions are ONE calendar object: the master VEVENT carries
// the RRULE, and each changed instance is a further VEVENT carrying the same UID
// and a RECURRENCE-ID naming the instance it replaces. An update for one instance
// arrives as an object holding only that second kind, so folding it into the
// stored object is what keeps the series intact; storing it on its own would
// either replace the series or duplicate the instance.

// OccurrenceInstant returns the instance an object overrides: the RECURRENCE-ID of
// its single VEVENT. ok is false when the object carries a series master (it is a
// series, not one instance of one) or no RECURRENCE-ID at all.
func OccurrenceInstant(ical []byte) (time.Time, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return time.Time{}, false
	}
	return occurrenceInstantOf(cal)
}

// occurrenceInstantOf is OccurrenceInstant on an already-parsed object.
func occurrenceInstantOf(cal *icomp) (time.Time, bool) {
	var at time.Time
	found := false
	for _, c := range cal.comps {
		if c.name != "VEVENT" {
			continue
		}
		rid := c.prop("RECURRENCE-ID")
		if rid == nil {
			return time.Time{}, false // a master rides along: this is a series
		}
		if found {
			return time.Time{}, false // more than one instance: not a single update
		}
		t, _, ok := parseICalTime(rid)
		if !ok {
			return time.Time{}, false
		}
		at, found = t, true
	}
	return at, found
}

// MergeOverride folds an occurrence update into a stored recurring object: the
// update's VEVENT replaces the stored override for the same instance, or is added
// when the object carries none for it. Every other component is kept verbatim, and
// a VTIMEZONE the update needs but the stored object lacks is carried over, because
// an instance whose DTSTART names an unknown TZID cannot be placed. ok is false
// when the stored object carries no series master or the update is not one
// occurrence, so the caller leaves the stored object alone.
func MergeOverride(stored, update []byte) ([]byte, bool) {
	storedCal, err := parseICal(stored)
	if err != nil {
		return nil, false
	}
	updateCal, err := parseICal(update)
	if err != nil {
		return nil, false
	}
	if master, _ := findSeriesMaster(storedCal); master == nil {
		return nil, false
	}
	at, ok := occurrenceInstantOf(updateCal)
	if !ok {
		return nil, false
	}
	override := overrideAt(updateCal, at)
	if override == nil {
		return nil, false
	}

	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range storedCal.props {
		b.add(renderIline(l))
	}
	for _, c := range storedCal.comps {
		if isOverrideAt(c, at) {
			continue // replaced below, in the stored object's own component order
		}
		writeComponent(b, c)
	}
	for _, tz := range missingTimezones(storedCal, updateCal) {
		writeComponent(b, tz)
	}
	writeComponent(b, override)
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// CancelOccurrence removes one instance from a stored recurring object: its
// override is dropped and the master gains an EXDATE for the instant, so every
// reader stops showing it. ok is false when the object carries no series master.
func CancelOccurrence(stored []byte, at time.Time) ([]byte, bool) {
	cal, err := parseICal(stored)
	if err != nil {
		return nil, false
	}
	master, _ := findSeriesMaster(cal)
	if master == nil {
		return nil, false
	}
	_, allDay, _ := parseICalTime(master.prop("DTSTART"))

	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range cal.props {
		b.add(renderIline(l))
	}
	for _, c := range cal.comps {
		if isOverrideAt(c, at) {
			continue
		}
		if c == master {
			writeMasterWithExdate(b, master, at, allDay)
			continue
		}
		writeComponent(b, c)
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// writeMasterWithExdate emits the master with one EXDATE added, unless it already
// excludes the instant.
func writeMasterWithExdate(b *builder, master *icomp, at time.Time, allDay bool) {
	b.add("BEGIN:VEVENT")
	for _, l := range master.props {
		b.add(renderIline(l))
	}
	if !excludedInstants(master)[instantKey(at)] {
		b.add(dtLine("EXDATE", at, allDay))
	}
	for _, sub := range master.comps {
		writeComponent(b, sub)
	}
	b.add("END:VEVENT")
}

// overrideAt returns the object's VEVENT overriding the given instant, or nil.
func overrideAt(cal *icomp, at time.Time) *icomp {
	for _, c := range cal.comps {
		if isOverrideAt(c, at) {
			return c
		}
	}
	return nil
}

// isOverrideAt reports whether a component is the VEVENT overriding an instant.
// The comparison is on the parsed instant, so a RECURRENCE-ID written in UTC and
// one written with a TZID match when they name the same moment.
func isOverrideAt(c *icomp, at time.Time) bool {
	if c.name != "VEVENT" {
		return false
	}
	rid := c.prop("RECURRENCE-ID")
	if rid == nil {
		return false
	}
	t, _, ok := parseICalTime(rid)
	return ok && instantKey(t) == instantKey(at)
}

// missingTimezones returns the update's VTIMEZONE components whose TZID the stored
// object does not already define.
func missingTimezones(stored, update *icomp) []*icomp {
	have := map[string]bool{}
	for _, c := range stored.comps {
		if c.name == "VTIMEZONE" {
			have[strings.ToUpper(c.propText("TZID"))] = true
		}
	}
	var out []*icomp
	for _, c := range update.comps {
		if c.name != "VTIMEZONE" {
			continue
		}
		if id := strings.ToUpper(c.propText("TZID")); id != "" && !have[id] {
			have[id] = true
			out = append(out, c)
		}
	}
	return out
}
