package oxcical

import "time"

// HasInstance reports whether a stored series has a live instance generated at
// the instant at: the rule or an RDATE yields it, no EXDATE removes it, and no
// override cancels it. A moved instance still counts, because it is named by the
// instant its pattern generated.
func HasInstance(ical []byte, at time.Time) bool {
	cal, err := parseICal(ical)
	if err != nil {
		return false
	}
	master, overrides := splitSeries(cal)
	s, ok := seriesShape(master)
	if !ok {
		return false
	}
	skip := excludedInstants(master)
	if len(seriesInstants(master, s, skip, at, at.Add(time.Second))) == 0 {
		return false
	}
	_, live := instanceSpan(overrides[instantKey(at)], at, s.dur)
	return live
}

// MoveOccurrence moves one instance of a stored series to [start, end): it writes
// an override for the instant at, built from the instance's current override or,
// when it has none, from the master, so the instance keeps its title, place and
// attendees. Every other component is kept verbatim. ok is false when the object
// is not a series or has no live instance at at.
func MoveOccurrence(stored []byte, at, start, end time.Time) ([]byte, bool) {
	if !HasInstance(stored, at) {
		return nil, false
	}
	cal, err := parseICal(stored)
	if err != nil {
		return nil, false
	}
	master, _ := findSeriesMaster(cal)
	if master == nil {
		return nil, false
	}
	base := overrideAt(cal, at)
	if base == nil {
		base = master
	}
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range cal.props {
		b.add(renderIline(l))
	}
	for _, c := range cal.comps {
		if !isOverrideAt(c, at) {
			writeComponent(b, c)
		}
	}
	_, allDay, _ := parseICalTime(master.prop("DTSTART"))
	writeMovedInstance(b, base, instanceTimes{at: at, start: start, end: end, allDay: allDay})
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// instanceTimeLines are the lines a moved instance writes itself, so they are
// not copied from the component it is built from.
var instanceTimeLines = map[string]bool{
	"DTSTART": true, "DTEND": true, "DURATION": true, "DTSTAMP": true, "RECURRENCE-ID": true,
	"RRULE": true, "RDATE": true, "EXDATE": true,
}

// instanceTimes places one moved instance: the instant it replaces and its new
// span, written as dates for an all-day series.
type instanceTimes struct {
	at, start, end time.Time
	allDay         bool
}

// writeMovedInstance emits the override VEVENT for the instant it.at, placed at
// [it.start, it.end), carrying base's other properties and its alarm.
func writeMovedInstance(b *builder, base *icomp, it instanceTimes) {
	b.add("BEGIN:VEVENT")
	for _, l := range base.props {
		if !instanceTimeLines[l.name] {
			b.add(renderIline(l))
		}
	}
	b.add("DTSTAMP:" + formatICalUTC(time.Now()))
	b.add(dtLine("RECURRENCE-ID", it.at, it.allDay))
	b.add(dtLine("DTSTART", it.start, it.allDay))
	b.add(dtLine("DTEND", it.end, it.allDay))
	for _, sub := range base.comps {
		writeComponent(b, sub)
	}
	b.add("END:VEVENT")
}
