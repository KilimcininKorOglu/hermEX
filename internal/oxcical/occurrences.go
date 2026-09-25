package oxcical

import (
	"slices"
	"strings"
	"time"
)

// Span is one instance of a recurring object: the half-open interval it occupies.
type Span struct {
	Start, End time.Time
}

// OccurrencesIn returns the spans a stored recurring object occupies within
// [rangeStart, rangeEnd), honoring RRULE and RDATE together, EXDATE,
// RECURRENCE-ID overrides (an override's own time replaces the generated
// instance) and an override cancelled with STATUS:CANCELLED. ok is false when the
// object defines no recurrence set, so the caller reads the item's own stored
// start and end instead.
//
// TRANSP is not read: an instance counts as occupied whenever the item does, which
// is the busy status the caller already holds for the object as a whole.
func OccurrencesIn(ical []byte, rangeStart, rangeEnd time.Time) ([]Span, bool) {
	insts, ok := InstancesIn(ical, rangeStart, rangeEnd)
	if !ok {
		return nil, false
	}
	out := make([]Span, len(insts))
	for i, in := range insts {
		out[i] = in.Span
	}
	return out, true
}

// Instance is one occurrence of a recurring object: the instant the series
// generates for it, which is its RECURRENCE-ID and names it for an override or a
// cancellation, and the span it occupies, which an override may have moved.
type Instance struct {
	At time.Time
	Span
}

// InstancesIn is OccurrencesIn with each span's generated instant, in start order.
func InstancesIn(ical []byte, rangeStart, rangeEnd time.Time) ([]Instance, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	master, overrides := splitSeries(cal)
	s, ok := seriesShape(master)
	if !ok {
		return nil, false
	}
	skip := excludedInstants(master)
	out := overrideInstances(overrides, skip, s.dur, rangeStart, rangeEnd)
	for _, t := range seriesInstants(master, s, skip, rangeStart, rangeEnd) {
		if _, overridden := overrides[instantKey(t)]; overridden {
			continue // already tested at its own time
		}
		if span := (Span{Start: t, End: t.Add(s.dur)}); overlapsRange(span.Start, span.End, rangeStart, rangeEnd) {
			out = append(out, Instance{At: t, Span: span})
		}
	}
	slices.SortFunc(out, func(a, b Instance) int { return a.Start.Compare(b.Start) })
	return out, true
}

// series is what an expansion needs from the master: its first start, whether
// that start is a date without a time, one instance's duration, and the parsed
// rule. hasRule is false for a series defined by RDATE alone, which carries no
// RRULE to expand.
type series struct {
	start   time.Time
	allDay  bool
	dur     time.Duration
	rec     Recurrence
	hasRule bool
}

// seriesShape reads the master's expansion inputs. ok is false when the component
// is not a readable series, so the caller answers "not a series" rather than
// expanding nothing.
func seriesShape(master *icomp) (series, bool) {
	if master == nil {
		return series{}, false
	}
	dtstart := master.prop("DTSTART")
	start, allDay, ok := parseICalTime(dtstart)
	if !ok {
		return series{}, false
	}
	if !allDay && dtstart.loc != nil {
		// A rule repeats on the wall clock of the zone DTSTART names (RFC 5545
		// section 3.3.10), so the instants are generated there: 09:00 stays 09:00
		// across a daylight saving switch rather than keeping its UTC offset.
		start = start.In(dtstart.loc)
	}
	s := series{start: start, allDay: allDay}
	switch rrule := master.prop("RRULE"); {
	case rrule != nil:
		rec, ok := parseRRule(rrule.value)
		if !ok {
			return series{}, false
		}
		s.rec, s.hasRule = rec, true
	case len(addedInstants(master)) == 0:
		// Neither a rule nor an added date: a single event, not a series.
		return series{}, false
	}
	end, eok := eventEnd(master, start, allDay)
	if !eok {
		end = start
	}
	s.dur = end.Sub(start)
	return s, true
}

// seriesInstants returns the instance starts the master defines within
// [rangeStart, rangeEnd), in chronological order: the RRULE expansion plus every
// RDATE instant, with the EXDATE instants removed. RFC 5545 section 3.8.5 makes
// RDATE and RRULE two halves of one recurrence set, and EXDATE removes an
// instance from either half.
func seriesInstants(master *icomp, s series, skip map[string]bool, rangeStart, rangeEnd time.Time) []time.Time {
	var out []time.Time
	seen := map[string]bool{}
	add := func(t time.Time) {
		key := instantKey(t)
		if skip[key] || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, t)
	}
	inRange := func(t time.Time) bool { return !t.Before(rangeStart) && t.Before(rangeEnd) }
	if s.hasRule {
		for _, t := range s.rec.Occurrences(s.start, rangeStart, rangeEnd, 4096) {
			add(t)
		}
	} else if inRange(s.start) {
		// A rule expansion already yields DTSTART; an RDATE-only set states it
		// once in DTSTART and never repeats it (RFC 5545 section 3.8.5.2).
		add(s.start)
	}
	for _, t := range addedInstants(master) {
		if inRange(t) {
			add(t)
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// addedInstants collects the occurrences the master's RDATE lines add. A PERIOD
// value is not read, because an instance carries the series duration here and a
// period states a duration of its own.
func addedInstants(master *icomp) []time.Time {
	var out []time.Time
	for _, l := range master.propLines("RDATE") {
		for v := range strings.SplitSeq(l.value, ",") {
			v = strings.TrimSpace(v)
			if v == "" || strings.Contains(v, "/") {
				continue
			}
			rd := iline{name: "RDATE", params: l.params, value: v, loc: l.loc}
			if t, _, ok := parseICalTime(&rd); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// overrideInstances returns the instances the object's overrides occupy in the
// window. An override carries its own time and may sit anywhere, including outside
// the span its generated instant would have had, so each one is tested directly
// rather than through the enumeration.
func overrideInstances(overrides map[string]*icomp, skip map[string]bool, dur time.Duration, rangeStart, rangeEnd time.Time) []Instance {
	var out []Instance
	for key, ov := range overrides {
		if skip[key] {
			continue
		}
		at, _, ok := parseICalTime(ov.prop("RECURRENCE-ID"))
		if !ok {
			continue
		}
		if span, live := instanceSpan(ov, at, dur); live && overlapsRange(span.Start, span.End, rangeStart, rangeEnd) {
			out = append(out, Instance{At: at, Span: span})
		}
	}
	return out
}

// instanceSpan resolves one instance to the interval it occupies: the override's
// own span when there is one, else the generated instant plus the series duration.
// live is false for an instance whose override cancels it.
func instanceSpan(override *icomp, at time.Time, dur time.Duration) (Span, bool) {
	if override == nil {
		return Span{Start: at, End: at.Add(dur)}, true
	}
	if strings.EqualFold(strings.TrimSpace(override.propText("STATUS")), "CANCELLED") {
		return Span{}, false
	}
	s, allDay, ok := parseICalTime(override.prop("DTSTART"))
	if !ok {
		return Span{Start: at, End: at.Add(dur)}, true
	}
	e, eok := eventEnd(override, s, allDay)
	if !eok {
		e = s.Add(dur)
	}
	return Span{Start: s, End: e}, true
}
