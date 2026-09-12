package oxcical

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// vzone is a VTIMEZONE reduced to what reading a date-time needs: the rules that
// switch the zone's UTC offset. A stream that names a zone no table knows still
// describes it here, so this is the last resort for such a TZID.
type vzone struct {
	tzid  string
	rules []vzoneRule
}

// vzoneRule is one STANDARD or DAYLIGHT block: the offset in force before and
// after it, the wall clock of its first onset, and, when it repeats, the yearly
// day it falls on.
type vzoneRule struct {
	from   int // UTC offset in seconds before the switch
	to     int // UTC offset in seconds after it
	start  time.Time
	yearly *yearlyOn
}

// yearlyOn is the month-and-day part of a FREQ=YEARLY transition rule, either a
// fixed day of the month or an ordinal weekday ("the last Sunday in October").
type yearlyOn struct {
	month time.Month
	day   int // day of month; 0 when the weekday fields are used
	wd    time.Weekday
	nth   int // BYDAY ordinal, negative counting back from the month's end
}

// onset is one candidate transition, ready to be ordered against a value.
type onset struct {
	at   time.Time
	to   int
	from int
}

// collectVZones parses the calendar's own VTIMEZONE components, keyed by TZID.
func collectVZones(root *icomp) map[string]*vzone {
	out := map[string]*vzone{}
	for _, c := range root.comps {
		if c.name != "VTIMEZONE" {
			continue
		}
		if z := parseVZone(c); z != nil {
			out[z.tzid] = z
		}
	}
	return out
}

// parseVZone reads one VTIMEZONE. A component with no usable rule is dropped, so
// its times stay unresolved and are reported rather than read at a guessed offset.
func parseVZone(vtz *icomp) *vzone {
	tzid := strings.TrimSpace(vtz.propText("TZID"))
	if tzid == "" {
		return nil
	}
	z := &vzone{tzid: tzid}
	for _, sub := range vtz.comps {
		if sub.name != "STANDARD" && sub.name != "DAYLIGHT" {
			continue
		}
		if r, ok := parseVZoneRule(sub); ok {
			z.rules = append(z.rules, r)
		}
	}
	if len(z.rules) == 0 {
		return nil
	}
	return z
}

// parseVZoneRule reads one STANDARD or DAYLIGHT block. TZOFFSETTO and DTSTART are
// required; a missing TZOFFSETFROM is read as no change across the onset.
func parseVZoneRule(c *icomp) (vzoneRule, bool) {
	to, ok := parseUTCOffset(c.propText("TZOFFSETTO"))
	if !ok {
		return vzoneRule{}, false
	}
	from, ok := parseUTCOffset(c.propText("TZOFFSETFROM"))
	if !ok {
		from = to
	}
	start, err := time.Parse("20060102T150405", strings.TrimSpace(c.propText("DTSTART")))
	if err != nil {
		return vzoneRule{}, false
	}
	return vzoneRule{from: from, to: to, start: start, yearly: parseYearlyOn(c.propText("RRULE"), start)}, true
}

// parseYearlyOn reads a transition rule's RRULE. Only FREQ=YEARLY is recognized,
// which is what a VTIMEZONE carries; anything else leaves the rule as a single
// onset at its DTSTART.
func parseYearlyOn(rrule string, start time.Time) *yearlyOn {
	if rrule == "" {
		return nil
	}
	parts := map[string]string{}
	for p := range strings.SplitSeq(rrule, ";") {
		if k, v, ok := strings.Cut(p, "="); ok {
			parts[strings.ToUpper(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if !strings.EqualFold(parts["FREQ"], "YEARLY") {
		return nil
	}
	on := &yearlyOn{month: start.Month(), day: start.Day()}
	applyByRules(on, parts)
	return on
}

// applyByRules narrows a yearly rule with the BY parts that name its day. An
// ordinal weekday wins over a day of the month, since a rule carrying both means
// the weekday.
func applyByRules(on *yearlyOn, parts map[string]string) {
	if m, err := strconv.Atoi(parts["BYMONTH"]); err == nil && m >= 1 && m <= 12 {
		on.month = time.Month(m)
	}
	if d, err := strconv.Atoi(parts["BYMONTHDAY"]); err == nil && d >= 1 && d <= 31 {
		on.day = d
	}
	if nth, wd, ok := parseZoneByDay(parts["BYDAY"]); ok {
		on.nth, on.wd, on.day = nth, wd, 0
	}
}

// parseZoneByDay reads an ordinal weekday such as "-1SU" (the last Sunday) or
// "2SU" (the second). A BYDAY with no ordinal names no single transition day and
// is refused, which is why this is not the recurrence parser's multi-day parseByDay.
func parseZoneByDay(s string) (int, time.Weekday, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 3 {
		return 0, 0, false
	}
	wd, ok := weekdayToken(s[len(s)-2:])
	if !ok {
		return 0, 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-2])
	if err != nil || n == 0 || n < -5 || n > 5 {
		return 0, 0, false
	}
	return n, wd, true
}

// at returns the zone one wall-clock value must be read in: a fixed zone holding
// the offset in force at that local time. A value the rules cannot bound leaves
// the line unresolved.
//
// For a list value (RDATE, EXDATE) the first entry decides the offset for the
// whole line, so a single line whose entries straddle a transition takes one
// offset for all of them.
func (z *vzone) at(value string) *time.Location {
	v, ok := wallClock(value)
	if !ok {
		return nil
	}
	off, ok := z.offsetAt(v)
	if !ok {
		return nil
	}
	return time.FixedZone(z.tzid, off)
}

// offsetAt picks the offset the last transition at or before v switched to. A
// value earlier than every transition takes the offset in force before the first
// one.
func (z *vzone) offsetAt(v time.Time) (int, bool) {
	c := z.candidates(v.Year())
	if len(c) == 0 {
		return 0, false
	}
	slices.SortFunc(c, func(a, b onset) int { return a.at.Compare(b.at) })
	if v.Before(c[0].at) {
		return c[0].from, true
	}
	off := c[0].to
	for _, o := range c {
		if o.at.After(v) {
			break
		}
		off = o.to
	}
	return off, true
}

// candidates lists the transitions that can bound a value in year y: every rule's
// own first onset plus, for a repeating rule, its occurrence in the year before,
// the year itself and the year after.
func (z *vzone) candidates(y int) []onset {
	var out []onset
	for _, r := range z.rules {
		out = append(out, onset{at: r.start, to: r.to, from: r.from})
		if r.yearly == nil {
			continue
		}
		for _, yy := range []int{y - 1, y, y + 1} {
			if t, ok := r.yearly.on(yy, r.start); ok {
				out = append(out, onset{at: t, to: r.to, from: r.from})
			}
		}
	}
	return out
}

// on returns the rule's transition in year y, as a wall clock. The time of day
// always comes from the rule's DTSTART.
func (o yearlyOn) on(y int, start time.Time) (time.Time, bool) {
	h, m, s := start.Clock()
	if o.day > 0 {
		d := time.Date(y, o.month, o.day, h, m, s, 0, time.UTC)
		return d, d.Month() == o.month
	}
	return nthWeekdayOf(y, o.month, o.wd, o.nth, h, m, s)
}

// nthWeekdayOf returns the nth weekday of a month, counting back from its end for
// a negative n. An ordinal the month does not reach (a fifth Sunday it has no
// room for) has no occurrence that year.
func nthWeekdayOf(y int, mo time.Month, wd time.Weekday, nth, h, m, s int) (time.Time, bool) {
	var d time.Time
	if nth > 0 {
		d = time.Date(y, mo, 1, h, m, s, 0, time.UTC)
		d = d.AddDate(0, 0, (int(wd)-int(d.Weekday())+7)%7+(nth-1)*7)
	} else {
		d = time.Date(y, mo+1, 0, h, m, s, 0, time.UTC)
		d = d.AddDate(0, 0, -((int(d.Weekday())-int(wd)+7)%7)+(nth+1)*7)
	}
	return d, d.Month() == mo
}

// wallClock parses a zoneless date-time value (the first entry of a list value)
// into its wall-clock fields.
func wallClock(value string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	if len(v) != 15 || v[8] != 'T' {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405", v)
	return t, err == nil
}
