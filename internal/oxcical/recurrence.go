package oxcical

import (
	"strconv"
	"strings"
	"time"
)

// Recurrence is a calendar recurrence parsed from an iCalendar RRULE, expressed in
// the neutral terms a wire protocol like ActiveSync needs: the base frequency and
// interval, an end bound (a count or an until-instant, at most one), and the
// by-rules that pin the day(s).
type Recurrence struct {
	Freq     string    // "DAILY", "WEEKLY", "MONTHLY", or "YEARLY"
	Interval int       // every N units; 1 when RRULE omits INTERVAL
	Count    int       // total occurrences, or 0 when open-ended / UNTIL-bound
	Until    time.Time // last instant (UTC), or zero when open-ended / COUNT-bound
	Weekdays []string  // BYDAY weekday tokens (SU,MO,TU,WE,TH,FR,SA), ordinal stripped
	SetPos   int       // ordinal week (1..5, or -1 for last) of an nth-weekday rule, else 0
	MonthDay int       // BYMONTHDAY (1..31), or 0
	Month    int       // BYMONTH (1..12), or 0
}

// ParseRecurrence extracts the first VEVENT's start, end, and recurrence from a
// verbatim iCalendar object (the bytes oxcical preserves in PrIcalOriginal for a
// recurring event). ok is false when there is no VEVENT, no DTSTART, or no RRULE.
// The end is the first instance's end (DTEND, or DTSTART+DURATION, or DTSTART when
// neither is present), so even a zero-length instance carries one.
func ParseRecurrence(ical []byte) (start, end time.Time, rec Recurrence, ok bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return
	}
	vev := cal.sub("VEVENT")
	if vev == nil {
		return
	}
	dtstart := vev.prop("DTSTART")
	rrule := vev.prop("RRULE")
	if dtstart == nil || rrule == nil {
		return
	}
	st, allDay, sok := parseICalTime(dtstart)
	if !sok {
		return
	}
	en, eok := eventEnd(vev, st, allDay)
	if !eok {
		en = st
	}
	r, rok := parseRRule(rrule.value)
	if !rok {
		return
	}
	return st.UTC(), en.UTC(), r, true
}

// parseRRule parses an RRULE value (the text after "RRULE:") into a Recurrence. A
// value with no FREQ is rejected.
func parseRRule(value string) (Recurrence, bool) {
	r := Recurrence{Interval: 1}
	for part := range strings.SplitSeq(value, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(key)) {
		case "FREQ":
			r.Freq = strings.ToUpper(strings.TrimSpace(val))
		case "INTERVAL":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				r.Interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				r.Count = n
			}
		case "UNTIL":
			if t, ok := parseRRuleUntil(val); ok {
				r.Until = t
			}
		case "BYDAY":
			r.Weekdays, r.SetPos = parseByDay(val)
		case "BYMONTHDAY":
			if n, err := strconv.Atoi(val); err == nil {
				r.MonthDay = n
			}
		case "BYMONTH":
			if n, err := strconv.Atoi(val); err == nil {
				r.Month = n
			}
		case "BYSETPOS":
			if n, err := strconv.Atoi(val); err == nil {
				r.SetPos = n
			}
		}
	}
	if r.Freq == "" {
		return Recurrence{}, false
	}
	return r, true
}

// Occurrences enumerates the instance start instants of a recurrence that fall within
// [windowStart, windowEnd), given the series' first start (seriesStart, the master
// DTSTART). It honors INTERVAL, COUNT, and UNTIL, plus BYDAY for a weekly rule (each
// listed weekday). The nth-weekday refinements (ordinal BYDAY / BYSETPOS / BYMONTHDAY)
// are not applied, so such a rule expands on its base frequency only. limit bounds the
// total instances scanned, a safety stop for an open-ended (no COUNT/UNTIL) rule.
// Each returned instant carries seriesStart's clock time. EXDATE and RECURRENCE-ID
// overrides are the caller's concern (it holds the full component).
func (r Recurrence) Occurrences(seriesStart, windowStart, windowEnd time.Time, limit int) []time.Time {
	if limit <= 0 {
		limit = 4096
	}
	interval := max(r.Interval, 1)
	var out []time.Time
	emitted := 0 // counts series occurrences toward COUNT, including pre-window ones
	add := func(t time.Time) bool {
		if r.Count > 0 && emitted >= r.Count {
			return false
		}
		if !r.Until.IsZero() && t.After(r.Until) {
			return false
		}
		emitted++
		if !t.Before(windowStart) && t.Before(windowEnd) {
			out = append(out, t)
		}
		return true
	}

	switch strings.ToUpper(strings.TrimSpace(r.Freq)) {
	case "WEEKLY":
		if len(r.Weekdays) > 0 {
			r.weeklyByDay(seriesStart, windowEnd, interval, limit, add)
		} else {
			r.step(windowEnd, limit, add, func(i int) time.Time { return seriesStart.AddDate(0, 0, 7*interval*i) })
		}
	case "DAILY":
		r.step(windowEnd, limit, add, func(i int) time.Time { return seriesStart.AddDate(0, 0, interval*i) })
	case "MONTHLY":
		r.step(windowEnd, limit, add, func(i int) time.Time { return seriesStart.AddDate(0, interval*i, 0) })
	case "YEARLY":
		r.step(windowEnd, limit, add, func(i int) time.Time { return seriesStart.AddDate(interval*i, 0, 0) })
	}
	return out
}

// step walks i = 0, 1, 2, ... feeding gen(i) into add until add stops (COUNT/UNTIL),
// the instance passes windowEnd, or the limit is hit.
func (r Recurrence) step(windowEnd time.Time, limit int, add func(time.Time) bool, gen func(int) time.Time) {
	for i := range limit {
		t := gen(i)
		if !add(t) {
			return
		}
		if t.After(windowEnd) {
			return
		}
	}
}

// weeklyByDay enumerates a weekly BYDAY rule: within each interval-week period (from
// the week of seriesStart), every listed weekday on or after seriesStart is an
// instance, at seriesStart's clock time.
func (r Recurrence) weeklyByDay(seriesStart, windowEnd time.Time, interval, limit int, add func(time.Time) bool) {
	want := map[time.Weekday]bool{}
	for _, d := range r.Weekdays {
		if wd, ok := weekdayToken(d); ok {
			want[wd] = true
		}
	}
	if len(want) == 0 {
		return
	}
	weekStart := weekStartMonday(seriesStart)
	scanned := 0
	for period := range limit {
		base := weekStart.AddDate(0, 0, 7*interval*period)
		for off := range 7 {
			day := base.AddDate(0, 0, off)
			if !want[day.Weekday()] {
				continue
			}
			inst := time.Date(day.Year(), day.Month(), day.Day(),
				seriesStart.Hour(), seriesStart.Minute(), seriesStart.Second(), 0, seriesStart.Location())
			if inst.Before(seriesStart) {
				continue
			}
			if !add(inst) {
				return
			}
			if scanned++; scanned >= limit {
				return
			}
		}
		if base.After(windowEnd) {
			return
		}
	}
}

// weekStartMonday returns midnight of the Monday of t's week, in t's location.
func weekStartMonday(t time.Time) time.Time {
	days := (int(t.Weekday()) + 6) % 7 // Mon=0 .. Sun=6
	d := t.AddDate(0, 0, -days)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
}

// weekdayToken maps a BYDAY weekday token to a time.Weekday.
func weekdayToken(s string) (time.Weekday, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SU":
		return time.Sunday, true
	case "MO":
		return time.Monday, true
	case "TU":
		return time.Tuesday, true
	case "WE":
		return time.Wednesday, true
	case "TH":
		return time.Thursday, true
	case "FR":
		return time.Friday, true
	case "SA":
		return time.Saturday, true
	}
	return 0, false
}

// parseByDay splits a BYDAY value (e.g. "MO,WE,FR" or "2MO") into its weekday
// tokens, returning any single ordinal prefix it carries (the nth-weekday rule).
func parseByDay(val string) (days []string, setPos int) {
	for tok := range strings.SplitSeq(val, ",") {
		tok = strings.TrimSpace(tok)
		i := 0
		for i < len(tok) && (tok[i] == '+' || tok[i] == '-' || (tok[i] >= '0' && tok[i] <= '9')) {
			i++
		}
		if i > 0 {
			if n, err := strconv.Atoi(tok[:i]); err == nil {
				setPos = n
			}
		}
		if day := strings.ToUpper(tok[i:]); day != "" {
			days = append(days, day)
		}
	}
	return days, setPos
}

// parseRRuleUntil parses an UNTIL value, which may be a UTC datetime, a local
// datetime, or a date.
func parseRRuleUntil(val string) (time.Time, bool) {
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, val); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
