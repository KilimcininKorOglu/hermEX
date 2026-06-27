package oxcical

import (
	"strings"
	"time"
)

// LimitRecurrenceSet trims a recurring object's calendar-data to the master component
// plus only the overridden components (those carrying a RECURRENCE-ID) that impact
// [rangeStart, rangeEnd), per CALDAV:limit-recurrence-set (RFC 4791 §9.6.6). Unlike
// expand it does NOT enumerate: the master keeps its RRULE. An override impacts the
// range when its current DTSTART/DTEND overlap it, or when its original instant
// (RECURRENCE-ID + master duration) overlaps it. Non-VEVENT components and the master
// are always kept. ok is false when there is no recurring master with overrides, so
// the caller serves the data unchanged.
func LimitRecurrenceSet(ical []byte, rangeStart, rangeEnd time.Time) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	var master *icomp
	hasOverride := false
	for _, c := range cal.comps {
		if c.name != "VEVENT" {
			continue
		}
		if c.prop("RECURRENCE-ID") != nil {
			hasOverride = true
		} else if c.prop("RRULE") != nil {
			master = c
		}
	}
	if master == nil || !hasOverride {
		return nil, false
	}
	var masterDur time.Duration
	if ds := master.prop("DTSTART"); ds != nil {
		if s, allDay, ok := parseICalTime(ds); ok {
			if e, eok := eventEnd(master, s, allDay); eok {
				masterDur = e.Sub(s)
			}
		}
	}

	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range cal.props {
		b.add(renderIline(l))
	}
	for _, c := range cal.comps {
		if c.name == "VEVENT" && c.prop("RECURRENCE-ID") != nil && !overrideImpacts(c, masterDur, rangeStart, rangeEnd) {
			continue
		}
		writeComponent(b, c)
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// overrideImpacts reports whether an overridden VEVENT impacts [rangeStart, rangeEnd):
// its current span overlaps, or its original instant (RECURRENCE-ID + the master's
// duration) overlaps.
func overrideImpacts(c *icomp, masterDur time.Duration, rangeStart, rangeEnd time.Time) bool {
	if ds := c.prop("DTSTART"); ds != nil {
		if s, allDay, ok := parseICalTime(ds); ok {
			e, eok := eventEnd(c, s, allDay)
			if !eok {
				e = s
			}
			if overlapsRange(s, e, rangeStart, rangeEnd) {
				return true
			}
		}
	}
	if rid := c.prop("RECURRENCE-ID"); rid != nil {
		if s, _, ok := parseICalTime(rid); ok && overlapsRange(s, s.Add(masterDur), rangeStart, rangeEnd) {
			return true
		}
	}
	return false
}

// overlapsRange reports whether [s, e) overlaps [rs, re); a zero-length span is treated
// as the point s. The range start is inclusive, the end non-inclusive.
func overlapsRange(s, e, rs, re time.Time) bool {
	if !e.After(s) {
		return !s.Before(rs) && s.Before(re)
	}
	return s.Before(re) && e.After(rs)
}

// LimitFreeBusySet trims each VFREEBUSY component to the FREEBUSY periods that intersect
// [rangeStart, rangeEnd), per CALDAV:limit-freebusy-set (RFC 4791 §9.6.7). A FREEBUSY
// property whose periods all fall outside the range is dropped; other properties are
// untouched. ok is false when the object has no VFREEBUSY, so the caller serves it
// unchanged.
func LimitFreeBusySet(ical []byte, rangeStart, rangeEnd time.Time) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	hasFB := false
	for _, c := range cal.comps {
		if c.name == "VFREEBUSY" {
			hasFB = true
			break
		}
	}
	if !hasFB {
		return nil, false
	}
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range cal.props {
		b.add(renderIline(l))
	}
	for _, c := range cal.comps {
		if c.name != "VFREEBUSY" {
			writeComponent(b, c)
			continue
		}
		b.add("BEGIN:VFREEBUSY")
		for _, l := range c.props {
			if l.name != "FREEBUSY" {
				b.add(renderIline(l))
				continue
			}
			if kept := filterFreeBusyPeriods(l.value, rangeStart, rangeEnd); kept != "" {
				b.add(renderIline(iline{name: "FREEBUSY", params: l.params, value: kept}))
			}
		}
		for _, sub := range c.comps {
			writeComponent(b, sub)
		}
		b.add("END:VFREEBUSY")
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// filterFreeBusyPeriods keeps the comma-separated FREEBUSY periods that intersect
// [rs, re); an unparseable period is kept verbatim (conservative).
func filterFreeBusyPeriods(value string, rs, re time.Time) string {
	var kept []string
	for p := range strings.SplitSeq(value, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		s, e, ok := parseFreeBusyPeriod(p)
		if !ok || overlapsRange(s, e, rs, re) {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ",")
}

// parseFreeBusyPeriod parses an RFC 5545 PERIOD ("start/end" or "start/duration"); both
// the start and an explicit end are UTC DATE-TIMEs.
func parseFreeBusyPeriod(p string) (start, end time.Time, ok bool) {
	a, d, found := strings.Cut(p, "/")
	if !found {
		return time.Time{}, time.Time{}, false
	}
	s, err := time.Parse("20060102T150405Z", strings.TrimSpace(a))
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	d = strings.TrimSpace(d)
	if strings.HasPrefix(d, "P") {
		dur, dok := parseICalDuration(d)
		if !dok {
			return time.Time{}, time.Time{}, false
		}
		return s.UTC(), s.Add(dur).UTC(), true
	}
	e, err := time.Parse("20060102T150405Z", d)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return s.UTC(), e.UTC(), true
}
