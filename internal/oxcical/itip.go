package oxcical

import (
	"strconv"
	"strings"
	"time"
)

// iTIP (RFC 5546) bodies built from a stored calendar object. The organizer sends
// the whole object for a meeting request, one VEVENT with no RECURRENCE-ID to
// cancel the whole meeting, and one VEVENT carrying a RECURRENCE-ID to change or
// cancel a single instance. An attendee applies a single-instance message to its
// own copy of the series (MergeOverride, CancelInstance) and the whole-meeting
// messages to the copy itself.

// WithMethod returns the object with its METHOD set to method, replacing any METHOD
// it already carries. ok is false when the object does not parse.
func WithMethod(ical []byte, method string) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	b := &builder{}
	writeCalendarHead(b, cal, method)
	for _, c := range cal.comps {
		writeComponent(b, c)
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// WithUID returns the object with every VEVENT's UID set to uid, for a meeting its
// attendees already hold under a different identity than the stored one. ok is
// false when the object does not parse.
func WithUID(ical []byte, uid string) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	for _, c := range cal.comps {
		if c.name == "VEVENT" {
			setProp(c, "UID", escapeValue(uid))
		}
	}
	return renderCalendar(cal), true
}

// CancelBody renders a METHOD:CANCEL for the whole meeting the object describes: its
// time zones and its primary VEVENT (the series master, or the event itself) marked
// STATUS:CANCELLED at sequence seq. The overrides are left out, because a
// cancellation with no RECURRENCE-ID already covers every instance. ok is false when
// the object does not parse or carries no event.
func CancelBody(ical []byte, seq int) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	ev := primaryEvent(cal)
	if ev == nil {
		return nil, false
	}
	b := &builder{}
	writeCalendarHead(b, cal, "CANCEL")
	writeTimezones(b, cal)
	writeComponent(b, cancelled(cloneComp(ev), seq))
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// InstanceBody renders a single-instance iTIP message with the given method for the
// instance generated at at: its override when the object has one, else the instance
// the master places there. The VEVENT carries RECURRENCE-ID and sequence seq; a
// CANCEL marks it STATUS:CANCELLED. The object's time zones ride along, because an
// override may name one. ok is false when the object is not a series or has no
// such instance.
func InstanceBody(stored []byte, at time.Time, method string, seq int) ([]byte, bool) {
	cal, master, s, ok := parseSeries(stored)
	if !ok {
		return nil, false
	}
	if !generates(master, s, at) {
		return nil, false
	}
	ev := instanceComp(master, overrideAt(cal, at), at, s)
	if strings.EqualFold(method, "CANCEL") {
		ev = cancelled(ev, seq)
	} else {
		setProp(ev, "SEQUENCE", strconv.Itoa(seq))
		setProp(ev, "DTSTAMP", formatICalUTC(time.Now()))
	}
	b := &builder{}
	writeCalendarHead(b, cal, strings.ToUpper(method))
	writeTimezones(b, cal)
	writeComponent(b, ev)
	b.add("END:VCALENDAR")
	return b.buf.Bytes(), true
}

// CancelInstance marks one instance of a stored series cancelled, the way an
// attendee's copy records an organizer's cancellation of that instance: the
// instance keeps its place in the calendar with STATUS:CANCELLED, so its owner sees
// it was called off and removes it. An instance already removed or cancelled is not
// recreated. ok is false when the object is not a series or has no live instance at
// at.
func CancelInstance(stored []byte, at time.Time) ([]byte, bool) {
	if !HasInstance(stored, at) {
		return nil, false
	}
	cal, master, s, ok := parseSeries(stored)
	if !ok {
		return nil, false
	}
	ev := instanceComp(master, overrideAt(cal, at), at, s)
	setProp(ev, "STATUS", "CANCELLED")
	return replaceOverride(cal, at, ev), true
}

// Sequence returns the SEQUENCE of the object's primary VEVENT, or of the override
// for *at when at is set and the object has one. A missing or unreadable value is 0,
// the RFC 5545 default.
func Sequence(ical []byte, at *time.Time) int {
	cal, err := parseICal(ical)
	if err != nil {
		return 0
	}
	ev := primaryEvent(cal)
	if at != nil {
		if ov := overrideAt(cal, *at); ov != nil {
			ev = ov
		}
	}
	if ev == nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(ev.propText("SEQUENCE")))
	return n
}

// SetSequence writes seq as the SEQUENCE of the object's primary VEVENT, or of the
// override for *at when at is set. ok is false when the object does not parse or has
// no such component.
func SetSequence(ical []byte, seq int, at *time.Time) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	target := primaryEvent(cal)
	if at != nil {
		target = overrideAt(cal, *at)
	}
	if target == nil {
		return nil, false
	}
	setProp(target, "SEQUENCE", strconv.Itoa(seq))
	return renderCalendar(cal), true
}

// renderCalendar serializes a parsed calendar as it stands.
func renderCalendar(cal *icomp) []byte {
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range cal.props {
		b.add(renderIline(l))
	}
	for _, c := range cal.comps {
		writeComponent(b, c)
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}

// parseSeries parses a stored series and reads its master's expansion inputs.
func parseSeries(stored []byte) (*icomp, *icomp, series, bool) {
	cal, err := parseICal(stored)
	if err != nil {
		return nil, nil, series{}, false
	}
	master, _ := findSeriesMaster(cal)
	s, ok := seriesShape(master)
	if !ok {
		return nil, nil, series{}, false
	}
	return cal, master, s, true
}

// generates reports whether the series generates an instance at at and no EXDATE
// removes it, whatever its override says about it.
func generates(master *icomp, s series, at time.Time) bool {
	return len(seriesInstants(master, s, excludedInstants(master), at, at.Add(time.Second))) > 0
}

// primaryEvent is the object's series master, or its first VEVENT that overrides
// nothing.
func primaryEvent(cal *icomp) *icomp {
	if master, _ := findSeriesMaster(cal); master != nil {
		return master
	}
	for _, c := range cal.comps {
		if c.name == "VEVENT" && c.prop("RECURRENCE-ID") == nil {
			return c
		}
	}
	return nil
}

// instanceComp builds the VEVENT for one instance: a copy of its override when
// there is one, else the master placed at the instance, with the recurrence rules
// dropped and RECURRENCE-ID added.
func instanceComp(master, override *icomp, at time.Time, s series) *icomp {
	if override != nil {
		return cloneComp(override)
	}
	ev := &icomp{name: "VEVENT", comps: master.comps}
	for _, l := range master.props {
		if !instanceTimeLines[l.name] {
			ev.props = append(ev.props, l)
		}
	}
	ev.props = append(ev.props,
		contentLine("DTSTAMP:"+formatICalUTC(time.Now())),
		contentLine(dtLine("RECURRENCE-ID", at, s.allDay)),
		contentLine(dtLine("DTSTART", at, s.allDay)),
		contentLine(dtLine("DTEND", at.Add(s.dur), s.allDay)),
	)
	return ev
}

// cancelled marks a VEVENT as a cancellation at sequence seq. Its alarms are
// dropped, because a cancelled event must not remind anyone.
func cancelled(ev *icomp, seq int) *icomp {
	setProp(ev, "STATUS", "CANCELLED")
	setProp(ev, "SEQUENCE", strconv.Itoa(seq))
	setProp(ev, "DTSTAMP", formatICalUTC(time.Now()))
	ev.comps = nil
	return ev
}

// replaceOverride renders the calendar with its override for at replaced by (or,
// when it had none, extended with) ev.
func replaceOverride(cal *icomp, at time.Time, ev *icomp) []byte {
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
	writeComponent(b, ev)
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}

// writeCalendarHead opens the VCALENDAR with its own properties, METHOD replaced by
// method.
func writeCalendarHead(b *builder, cal *icomp, method string) {
	b.add("BEGIN:VCALENDAR")
	for _, l := range cal.props {
		if l.name != "METHOD" {
			b.add(renderIline(l))
		}
	}
	b.add("METHOD:" + method)
}

// writeTimezones writes every VTIMEZONE the calendar defines.
func writeTimezones(b *builder, cal *icomp) {
	for _, c := range cal.comps {
		if c.name == "VTIMEZONE" {
			writeComponent(b, c)
		}
	}
}

// cloneComp copies a component's own line list, so editing the copy leaves the
// parsed original as it was. Nested components are shared, being written only.
func cloneComp(c *icomp) *icomp {
	return &icomp{name: c.name, props: append([]iline(nil), c.props...), comps: c.comps}
}

// setProp replaces every line named name with one carrying value (already in
// content-line form), or appends it when the component has none.
func setProp(c *icomp, name, value string) {
	kept := c.props[:0:0]
	for _, l := range c.props {
		if l.name != name {
			kept = append(kept, l)
		}
	}
	c.props = append(kept, iline{name: name, params: map[string][]string{}, value: value})
}

// contentLine parses one rendered content line back into its parts.
func contentLine(s string) iline {
	name, params, value := splitLine(s)
	return iline{name: strings.ToUpper(name), params: params, value: value}
}
