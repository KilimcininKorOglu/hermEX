package oxcical

import (
	"fmt"
	"time"
)

// VTimezone renders the VTIMEZONE component (RFC 5545 3.6.5) describing loc as
// its rules stand in year, one content line per element. RFC 5545 requires one
// for every TZID a calendar object uses, and a client that does not know the
// IANA name can place a time only through it.
//
// A zone with no offset change in the year gets one STANDARD block. A zone that
// switches twice, the usual daylight-saving shape, gets a DAYLIGHT and a STANDARD
// block, each repeating yearly on the ordinal weekday its switch fell on. Any
// other shape (a zone that abolished or introduced daylight saving that year)
// gets one non-repeating block per switch.
func VTimezone(loc *time.Location, year int) []string {
	lines := []string{"BEGIN:VTIMEZONE", "TZID:" + loc.String()}
	switches := zoneSwitches(loc, year)
	if len(switches) == 0 {
		_, off := time.Date(year, time.January, 1, 0, 0, 0, 0, loc).Zone()
		lines = append(lines, zoneBlock("STANDARD", "19700101T000000", off, off, "")...)
		return append(lines, "END:VTIMEZONE")
	}
	yearly := len(switches) == 2
	for _, s := range switches {
		rule := ""
		if yearly {
			rule = yearlyRule(s.wall)
		}
		lines = append(lines, zoneBlock(s.kind(), s.wall.Format("20060102T150405"), s.from, s.to, rule)...)
	}
	return append(lines, "END:VTIMEZONE")
}

// zoneSwitch is one change of a zone's UTC offset: the wall clock it happens at,
// read in the offset in force before it, and the offsets on either side.
type zoneSwitch struct {
	wall     time.Time
	from, to int
	dst      bool
}

// kind names the block a switch opens: DAYLIGHT when it enters daylight-saving
// time, STANDARD otherwise.
func (s zoneSwitch) kind() string {
	if s.dst {
		return "DAYLIGHT"
	}
	return "STANDARD"
}

// zoneSwitches finds every offset change in year. A day-long step finds the day,
// and halving the interval finds the minute, which is the resolution every zone
// rule is written in.
func zoneSwitches(loc *time.Location, year int) []zoneSwitch {
	var out []zoneSwitch
	end := time.Date(year+1, time.January, 1, 0, 0, 0, 0, loc)
	for t := time.Date(year, time.January, 1, 0, 0, 0, 0, loc); t.Before(end); t = t.Add(24 * time.Hour) {
		next := t.Add(24 * time.Hour)
		if offsetOf(t) == offsetOf(next) {
			continue
		}
		at := switchInstant(t, next)
		from, to := offsetOf(at.Add(-time.Minute)), offsetOf(at)
		// The switch to the larger offset is daylight time. tzdata marks a zone
		// with negative daylight saving (Europe/Dublin) the other way round, and
		// Windows, which reads what this writes, does not.
		out = append(out, zoneSwitch{
			wall: at.UTC().Add(time.Duration(from) * time.Second),
			from: from,
			to:   to,
			dst:  to > from,
		})
	}
	return out
}

// switchInstant narrows [a, b), across which the offset changes, to the first
// minute the new offset is in force.
func switchInstant(a, b time.Time) time.Time {
	for b.Sub(a) > time.Minute {
		mid := a.Add(b.Sub(a) / 2).Truncate(time.Minute)
		if mid.Equal(a) {
			break
		}
		if offsetOf(mid) == offsetOf(a) {
			a = mid
		} else {
			b = mid
		}
	}
	return b
}

// offsetOf is the UTC offset in force at t, in seconds east of UTC.
func offsetOf(t time.Time) int {
	_, off := t.Zone()
	return off
}

// yearlyRule is the RRULE repeating a switch every year on the same ordinal
// weekday of the same month: the last one (-1) when no later one fits in the
// month, which is how daylight-saving rules are written.
func yearlyRule(wall time.Time) string {
	nth := (wall.Day()-1)/7 + 1
	if wall.Day()+7 > daysIn(wall.Year(), wall.Month()) {
		nth = -1
	}
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", int(wall.Month()), nth, weekdayCode(wall.Weekday()))
}

// daysIn is the number of days in a month.
func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// weekdayCode is the RFC 5545 two-letter weekday.
func weekdayCode(wd time.Weekday) string {
	return [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[wd]
}

// zoneBlock renders one STANDARD or DAYLIGHT block.
func zoneBlock(kind, dtstart string, from, to int, rrule string) []string {
	lines := []string{"BEGIN:" + kind, "DTSTART:" + dtstart}
	if rrule != "" {
		lines = append(lines, "RRULE:"+rrule)
	}
	return append(lines, "TZOFFSETFROM:"+formatUTCOffset(from), "TZOFFSETTO:"+formatUTCOffset(to), "END:"+kind)
}

// formatUTCOffset renders seconds east of UTC as an RFC 5545 UTC-OFFSET, with the
// seconds part only when the offset has one.
func formatUTCOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	h, m, s := sec/3600, sec%3600/60, sec%60
	if s != 0 {
		return fmt.Sprintf("%s%02d%02d%02d", sign, h, m, s)
	}
	return fmt.Sprintf("%s%02d%02d", sign, h, m)
}
