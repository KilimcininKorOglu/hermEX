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
	out := overrideInstances(overrides, skip, s, rangeStart, rangeEnd)
	for _, t := range overlappingInstants(master, s, skip, rangeStart, rangeEnd) {
		if _, overridden := overrides[instantKey(t)]; overridden {
			continue // already tested at its own time
		}
		if span := (Span{Start: t, End: t.Add(s.durAt(t))}); overlapsRange(span.Start, span.End, rangeStart, rangeEnd) {
			out = append(out, Instance{At: t, Span: span})
		}
	}
	slices.SortFunc(out, func(a, b Instance) int { return a.Start.Compare(b.Start) })
	return out, true
}

// GeneratedInstants returns the instants a stored series' rule and added dates
// generate within [rangeStart, rangeEnd), in order, those an EXDATE or a
// cancellation has since removed included: the numbering an occurrence index
// counts in ([MS-OXWSCORE] OccurrenceItemId). ok is false when the object is not a
// series.
func GeneratedInstants(ical []byte, rangeStart, rangeEnd time.Time) ([]time.Time, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	master, _ := splitSeries(cal)
	s, ok := seriesShape(master)
	if !ok {
		return nil, false
	}
	return seriesInstants(master, s, nil, rangeStart, rangeEnd), true
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
	// periods holds the own duration of each instance an RDATE PERIOD adds,
	// keyed by instantKey.
	periods map[string]time.Duration
}

// durAt is the duration of the instance generated at t: a PERIOD states its own
// (RFC 5545 section 3.8.5.2), every other instance takes the series duration. A
// PERIOD that starts on an RRULE instant names that one instance, which the
// recurrence set counts once, so its stated duration is the one it keeps.
func (s series) durAt(t time.Time) time.Duration {
	if d, ok := s.periods[instantKey(t)]; ok {
		return d
	}
	return s.dur
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
	s := series{start: start, allDay: allDay, periods: addedPeriods(master, allDay)}
	switch rrule := master.prop("RRULE"); {
	case rrule != nil:
		rec, ok := parseRRule(rrule.value)
		if !ok {
			return series{}, false
		}
		s.rec, s.hasRule = rec, true
	case len(addedInstants(master, allDay)) == 0:
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
	for _, t := range addedInstants(master, s.allDay) {
		if inRange(t) {
			add(t)
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

// overlappingInstants returns the generated instants whose instance overlaps
// [rangeStart, rangeEnd), in order. An instance that starts before the window and
// is still running in it occupies the window too (RFC 4791 section 9.9 counts an
// event by overlap), so the generation reaches back by the longest instance.
func overlappingInstants(master *icomp, s series, skip map[string]bool, rangeStart, rangeEnd time.Time) []time.Time {
	var out []time.Time
	for _, t := range seriesInstants(master, s, skip, rangeStart.Add(-s.longest()), rangeEnd) {
		if overlapsRange(t, t.Add(s.durAt(t)), rangeStart, rangeEnd) {
			out = append(out, t)
		}
	}
	return out
}

// longest is the longest instance duration the series generates.
func (s series) longest() time.Duration {
	d := s.dur
	for _, p := range s.periods {
		d = max(d, p)
	}
	return d
}

// addedDate is one RDATE value: its start and, for a PERIOD, its own duration.
type addedDate struct {
	at     time.Time
	dur    time.Duration
	period bool
}

// addedDates reads the master's RDATE values. A PERIOD value (VALUE=PERIOD, RFC
// 5545 section 3.3.9) is "start/end" or "start/duration" and adds an instance of
// that length. A PERIOD is always a date-time, so an all-day series takes none,
// and one whose end is not after its start, or that does not parse, is dropped
// like any unreadable RDATE value.
func addedDates(master *icomp, allDay bool) []addedDate {
	var out []addedDate
	for _, l := range master.propLines("RDATE") {
		isPeriod := strings.EqualFold(l.param("VALUE"), "PERIOD")
		for v := range strings.SplitSeq(l.value, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if d, ok := readAddedDate(&l, v, isPeriod, allDay); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

// readAddedDate parses one RDATE value of line l.
func readAddedDate(l *iline, v string, isPeriod, allDay bool) (addedDate, bool) {
	if !isPeriod {
		if strings.Contains(v, "/") {
			return addedDate{}, false
		}
		t, _, ok := parseICalTime(&iline{name: "RDATE", params: l.params, value: v, loc: l.loc})
		return addedDate{at: t}, ok
	}
	if allDay {
		return addedDate{}, false
	}
	start, dur, ok := parsePeriodValue(l, v)
	return addedDate{at: start, dur: dur, period: true}, ok
}

// parsePeriodValue parses an RDATE PERIOD value in the line's zone: an explicit
// end or a duration after the start. ok is false unless the length is positive.
func parsePeriodValue(l *iline, v string) (time.Time, time.Duration, bool) {
	a, b, found := strings.Cut(v, "/")
	if !found {
		return time.Time{}, 0, false
	}
	start, ok := parseICalDateTime(l, strings.TrimSpace(a))
	if !ok {
		return time.Time{}, 0, false
	}
	b = strings.TrimSpace(b)
	var dur time.Duration
	if strings.HasPrefix(b, "P") || strings.HasPrefix(b, "+P") {
		dur, ok = parseICalDuration(strings.TrimPrefix(b, "+"))
	} else {
		var end time.Time
		end, ok = parseICalDateTime(l, b)
		dur = end.Sub(start)
	}
	if !ok || dur <= 0 {
		return time.Time{}, 0, false
	}
	return start, dur, true
}

// addedInstants returns the instants the master's RDATE values add.
func addedInstants(master *icomp, allDay bool) []time.Time {
	dates := addedDates(master, allDay)
	out := make([]time.Time, len(dates))
	for i, d := range dates {
		out[i] = d.at
	}
	return out
}

// addedPeriods maps each instant an RDATE PERIOD adds to its own duration.
func addedPeriods(master *icomp, allDay bool) map[string]time.Duration {
	var out map[string]time.Duration
	for _, d := range addedDates(master, allDay) {
		if d.period {
			if out == nil {
				out = map[string]time.Duration{}
			}
			out[instantKey(d.at)] = d.dur
		}
	}
	return out
}

// overrideInstances returns the instances the object's overrides occupy in the
// window. An override carries its own time and may sit anywhere, including outside
// the span its generated instant would have had, so each one is tested directly
// rather than through the enumeration.
func overrideInstances(overrides map[string]*icomp, skip map[string]bool, s series, rangeStart, rangeEnd time.Time) []Instance {
	var out []Instance
	for key, ov := range overrides {
		if skip[key] {
			continue
		}
		at, _, ok := parseICalTime(ov.prop("RECURRENCE-ID"))
		if !ok {
			continue
		}
		if span, live := instanceSpan(ov, at, s.durAt(at)); live && overlapsRange(span.Start, span.End, rangeStart, rangeEnd) {
			out = append(out, Instance{At: at, Span: span})
		}
	}
	return out
}

// CancelledInstancesIn returns the instances of a stored series within
// [rangeStart, rangeEnd) that an override cancels with STATUS:CANCELLED, each at
// the span the override gives it. InstancesIn leaves them out, because a
// cancelled instance occupies no time; a view that shows what was cancelled reads
// them here. An instance an EXDATE removes is gone rather than cancelled, so it is
// not returned. The result is empty when the object is not a series.
func CancelledInstancesIn(ical []byte, rangeStart, rangeEnd time.Time) []Instance {
	cal, err := parseICal(ical)
	if err != nil {
		return nil
	}
	master, overrides := splitSeries(cal)
	s, ok := seriesShape(master)
	if !ok {
		return nil
	}
	skip := excludedInstants(master)
	var out []Instance
	for key, ov := range overrides {
		at, _, ok := parseICalTime(ov.prop("RECURRENCE-ID"))
		if skip[key] || !ok || !isCancelledComp(ov) {
			continue
		}
		if span := overrideSpan(ov, at, s.durAt(at)); overlapsRange(span.Start, span.End, rangeStart, rangeEnd) {
			out = append(out, Instance{At: at, Span: span})
		}
	}
	slices.SortFunc(out, func(a, b Instance) int { return a.Start.Compare(b.Start) })
	return out
}

// instanceSpan resolves one instance to the interval it occupies: the override's
// own span when there is one, else the generated instant plus the series duration.
// live is false for an instance whose override cancels it.
func instanceSpan(override *icomp, at time.Time, dur time.Duration) (Span, bool) {
	if override == nil {
		return Span{Start: at, End: at.Add(dur)}, true
	}
	if isCancelledComp(override) {
		return Span{}, false
	}
	return overrideSpan(override, at, dur), true
}

// isCancelledComp reports whether a component carries STATUS:CANCELLED.
func isCancelledComp(c *icomp) bool {
	return strings.EqualFold(strings.TrimSpace(c.propText("STATUS")), "CANCELLED")
}

// overrideSpan is the interval an override gives its instance: its own start and
// end, falling back to the generated instant and the series duration for what it
// does not state.
func overrideSpan(override *icomp, at time.Time, dur time.Duration) Span {
	s, allDay, ok := parseICalTime(override.prop("DTSTART"))
	if !ok {
		return Span{Start: at, End: at.Add(dur)}
	}
	e, eok := eventEnd(override, s, allDay)
	if !eok {
		e = s.Add(dur)
	}
	return Span{Start: s, End: e}
}
