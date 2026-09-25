package oxcical

import "strings"

// CarryExceptions carries a series' exceptions from its stored form into an
// edited form of the same series: the stored master's EXDATE lines join the
// edited master, and the stored overrides follow it, with any VTIMEZONE they need
// that the edited form lacks. It does so only while the edit keeps the pattern,
// the same first start and the same rule, because an exception names an instance
// by the instant the pattern generated and a changed pattern generates other
// instants. In every other case, and when either form is not a series, the
// edited form is returned as it is.
func CarryExceptions(stored, updated []byte) []byte {
	storedCal, err := parseICal(stored)
	if err != nil {
		return updated
	}
	updatedCal, err := parseICal(updated)
	if err != nil {
		return updated
	}
	sm, _ := findSeriesMaster(storedCal)
	um, _ := findSeriesMaster(updatedCal)
	if sm == nil || um == nil || !samePattern(sm, um) {
		return updated
	}
	exdates := sm.propLines("EXDATE")
	overrides := storedOverrides(storedCal)
	if len(exdates) == 0 && len(overrides) == 0 {
		return updated
	}
	return withExceptions(storedCal, updatedCal, um, exdates, overrides)
}

// withExceptions renders the edited calendar with the stored exceptions added:
// the EXDATE lines on its master, and the overrides with the zones they need
// after it.
func withExceptions(storedCal, updatedCal, um *icomp, exdates []iline, overrides []*icomp) []byte {
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	for _, l := range updatedCal.props {
		b.add(renderIline(l))
	}
	for _, c := range updatedCal.comps {
		if c == um {
			writeMasterWithLines(b, um, exdates)
			continue
		}
		writeComponent(b, c)
	}
	for _, tz := range missingTimezones(updatedCal, storedCal) {
		writeComponent(b, tz)
	}
	for _, ov := range overrides {
		writeComponent(b, ov)
	}
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}

// samePattern reports whether two masters generate the same instants: the same
// first start and the same rule.
func samePattern(a, b *icomp) bool {
	as, aDay, aok := parseICalTime(a.prop("DTSTART"))
	bs, bDay, bok := parseICalTime(b.prop("DTSTART"))
	if !aok || !bok || aDay != bDay || !as.Equal(bs) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.propText("RRULE")), strings.TrimSpace(b.propText("RRULE")))
}

// storedOverrides returns the object's RECURRENCE-ID VEVENTs in stored order.
func storedOverrides(cal *icomp) []*icomp {
	var out []*icomp
	for _, c := range cal.comps {
		if c.name == "VEVENT" && c.prop("RECURRENCE-ID") != nil {
			out = append(out, c)
		}
	}
	return out
}

// writeMasterWithLines emits a master with extra content lines after its own.
func writeMasterWithLines(b *builder, master *icomp, extra []iline) {
	b.add("BEGIN:VEVENT")
	for _, l := range master.props {
		b.add(renderIline(l))
	}
	for _, l := range extra {
		b.add(renderIline(l))
	}
	for _, sub := range master.comps {
		writeComponent(b, sub)
	}
	b.add("END:VEVENT")
}
