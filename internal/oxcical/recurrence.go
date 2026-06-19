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
