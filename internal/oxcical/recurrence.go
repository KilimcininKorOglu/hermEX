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
	en := eventEnd(vev, st, allDay)
	r, rok := parseRRule(rrule.value)
	if !rok {
		return
	}
	return st.UTC(), en.UTC(), r, true
}

// ParseRRule parses an RRULE value (the text after "RRULE:") into a Recurrence. A
// value with no FREQ is rejected. Exported so a non-calendar PIM object that stores
// its recurrence as RRULE text (a task) can reach the same neutral recurrence shape
// a wire protocol consumes, without re-implementing the parser.
func ParseRRule(value string) (Recurrence, bool) {
	return parseRRule(value)
}

// parseRRule parses an RRULE value (the text after "RRULE:") into a Recurrence. A
// value with no FREQ, or with a named part whose value is not allowed, is rejected.
func parseRRule(value string) (Recurrence, bool) {
	r := Recurrence{Interval: 1}
	for part := range strings.SplitSeq(value, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		apply, known := rruleParts[strings.ToUpper(strings.TrimSpace(key))]
		if known && !apply(&r, val) {
			return Recurrence{}, false
		}
	}
	if r.Freq == "" {
		return Recurrence{}, false
	}
	return r, true
}

// rruleParts is the RRULE parameter vocabulary. A part the table does not name is
// ignored, so an unrecognized refinement never rejects the rule carrying it; a
// named part whose value RFC 5545 3.3.10 does not allow rejects the rule.
var rruleParts = map[string]func(*Recurrence, string) bool{
	"FREQ": func(r *Recurrence, v string) bool {
		r.Freq = strings.ToUpper(strings.TrimSpace(v))
		return true
	},
	"INTERVAL": func(r *Recurrence, v string) bool { return setPositiveInt(v, &r.Interval) },
	"COUNT":    func(r *Recurrence, v string) bool { return setPositiveInt(v, &r.Count) },
	"UNTIL": func(r *Recurrence, v string) bool {
		t, ok := parseRRuleUntil(v)
		r.Until = t
		return ok
	},
	"BYDAY":      setByDay,
	"BYMONTHDAY": func(r *Recurrence, v string) bool { return setInt(v, &r.MonthDay) },
	"BYMONTH":    func(r *Recurrence, v string) bool { return setInt(v, &r.Month) },
	"BYSETPOS":   func(r *Recurrence, v string) bool { return setInt(v, &r.SetPos) },
}

// rfc5545Weekdays are the weekday codes a BYDAY value may name (RFC 5545 3.3.10).
var rfc5545Weekdays = map[string]bool{"SU": true, "MO": true, "TU": true, "WE": true, "TH": true, "FR": true, "SA": true}

// setByDay stores the BYDAY weekdays and ordinal, refusing a token that names no
// weekday.
func setByDay(r *Recurrence, v string) bool {
	days, setPos := parseByDay(v)
	for _, d := range days {
		if !rfc5545Weekdays[d] {
			return false
		}
	}
	r.Weekdays, r.SetPos = days, setPos
	return true
}

// setInt stores an integer parameter, refusing a value that is not an integer.
func setInt(v string, dst *int) bool {
	n, err := strconv.Atoi(v)
	*dst = n
	return err == nil
}

// setPositiveInt stores a count-like parameter, which RFC 5545 makes a positive
// integer; anything else is refused.
func setPositiveInt(v string, dst *int) bool {
	n, err := strconv.Atoi(v)
	*dst = n
	return err == nil && n > 0
}

// maxRRuleInterval caps INTERVAL so multiplying by 7 days or 12 months and
// stepping across periods never overflows signed integer arithmetic.
const maxRRuleInterval = 100000

// Occurrences enumerates the instance start instants of a recurrence that fall within
// [windowStart, windowEnd), given the series' first start (seriesStart, the master
// DTSTART). It honors INTERVAL, COUNT, and UNTIL, plus BYDAY for a weekly rule (each
// listed weekday). A monthly or yearly rule applies its day pins: BYMONTHDAY (a
// negative value counts from the month's end), BYDAY with an ordinal or BYSETPOS
// (the nth of the listed weekdays, such as the second Tuesday or the last weekday),
// and BYMONTH for a yearly rule. A month that lacks the pinned day (the 31st in
// April) has no instance, per RFC 5545. limit bounds the periods scanned, a safety
// stop for an open-ended (no COUNT/UNTIL) rule.
// Each returned instant carries seriesStart's clock time. EXDATE and RECURRENCE-ID
// overrides are the caller's concern (it holds the full component).
func (r Recurrence) Occurrences(seriesStart, windowStart, windowEnd time.Time, limit int) []time.Time {
	if limit <= 0 {
		limit = 4096
	}
	interval := max(r.Interval, 1)
	if interval > maxRRuleInterval {
		return nil
	}
	sink := &occurrenceSink{count: r.Count, until: r.Until, windowStart: windowStart, windowEnd: windowEnd}
	add := sink.add

	switch strings.ToUpper(strings.TrimSpace(r.Freq)) {
	case "WEEKLY":
		if len(r.Weekdays) > 0 {
			r.weeklyByDay(sink, seriesStart, windowStart, windowEnd, interval, limit)
			break
		}
		r.everyDays(sink, seriesStart, windowStart, windowEnd, 7*interval, limit)
	case "DAILY":
		r.everyDays(sink, seriesStart, windowStart, windowEnd, interval, limit)
	case "MONTHLY":
		r.monthly(seriesStart, seriesStart.Month(), interval, windowEnd, limit, add)
	case "YEARLY":
		month := seriesStart.Month()
		if r.Month >= 1 && r.Month <= 12 {
			month = time.Month(r.Month)
		}
		r.monthly(seriesStart, month, 12*interval, windowEnd, limit, add)
	}
	return sink.out
}

// monthly walks one month per period, every months months from firstMonth of the
// series' first year, feeding the month's pinned days into add. It stops once the
// series ends, the period passes windowEnd, or the limit is reached.
func (r Recurrence) monthly(seriesStart time.Time, firstMonth time.Month, months int, windowEnd time.Time, limit int, add func(time.Time) bool) {
	loc := seriesStart.Location()
	for period := range limit {
		first := time.Date(seriesStart.Year(), firstMonth+time.Month(months*period), 1, 0, 0, 0, 0, loc)
		for _, day := range r.monthDays(first, seriesStart) {
			inst := time.Date(day.Year(), day.Month(), day.Day(),
				seriesStart.Hour(), seriesStart.Minute(), seriesStart.Second(), 0, loc)
			if inst.Before(seriesStart) {
				continue
			}
			if !add(inst) {
				return
			}
		}
		if first.After(windowEnd) {
			return
		}
	}
}

// monthDays returns the days of the month starting at first that the rule's day pins
// select, in date order.
func (r Recurrence) monthDays(first, seriesStart time.Time) []time.Time {
	if len(r.Weekdays) == 0 {
		day := r.MonthDay
		if day == 0 {
			day = seriesStart.Day()
		}
		if d, ok := monthDay(first, day); ok {
			return []time.Time{d}
		}
		return nil
	}
	days := weekdaysInMonth(first, wantedWeekdays(r.Weekdays))
	if r.SetPos == 0 {
		return days
	}
	return pickSetPos(days, r.SetPos)
}

// monthDay resolves a BYMONTHDAY value in the month starting at first; a negative
// value counts back from the last day. ok is false when the month has no such day.
func monthDay(first time.Time, day int) (time.Time, bool) {
	length := first.AddDate(0, 1, -1).Day()
	if day < 0 {
		day = length + day + 1
	}
	if day < 1 || day > length {
		return time.Time{}, false
	}
	return first.AddDate(0, 0, day-1), true
}

// weekdaysInMonth lists the days of the month starting at first that fall on a wanted
// weekday.
func weekdaysInMonth(first time.Time, want map[time.Weekday]bool) []time.Time {
	var days []time.Time
	for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
		if want[d.Weekday()] {
			days = append(days, d)
		}
	}
	return days
}

// pickSetPos selects the pos-th day (1-based, or counted from the end when negative),
// or nothing when the month has fewer.
func pickSetPos(days []time.Time, pos int) []time.Time {
	i := pos - 1
	if pos < 0 {
		i = len(days) + pos
	}
	if i < 0 || i >= len(days) {
		return nil
	}
	return days[i : i+1]
}

// occurrenceSink collects the instances an expansion produces, applying the
// series-wide COUNT and UNTIL bounds and keeping only what falls in the window.
type occurrenceSink struct {
	count                  int
	until                  time.Time
	windowStart, windowEnd time.Time
	emitted                int // counts series occurrences toward COUNT, including pre-window ones
	out                    []time.Time
}

// add offers one instance to the sink, reporting false once the series is over.
func (s *occurrenceSink) add(t time.Time) bool {
	if s.count > 0 && s.emitted >= s.count {
		return false
	}
	if !s.until.IsZero() && t.After(s.until) {
		return false
	}
	s.emitted++
	if !t.Before(s.windowStart) && t.Before(s.windowEnd) {
		s.out = append(s.out, t)
	}
	return true
}

// everyDays expands a rule whose instances are a fixed number of calendar days
// apart (DAILY, and WEEKLY without BYDAY), starting just before the window.
func (r Recurrence) everyDays(sink *occurrenceSink, seriesStart, windowStart, windowEnd time.Time, periodDays, limit int) {
	from, ok := sink.skipBefore(seriesStart, windowStart, periodDays)
	if !ok {
		return
	}
	r.step(from, windowEnd, limit, sink.add, func(i int) time.Time { return seriesStart.AddDate(0, 0, periodDays*i) })
}

// skipBefore returns the first step index worth generating for a rule that steps
// a fixed number of calendar days: the one just before the window, so the work
// bound is spent inside the window rather than on the years since DTSTART. The
// skipped instances all fall before the window and still count toward COUNT; ok
// is false when COUNT is used up before the window begins.
func (s *occurrenceSink) skipBefore(seriesStart, windowStart time.Time, periodDays int) (from int, ok bool) {
	from = max(civilDays(seriesStart, windowStart)/periodDays-1, 0)
	if s.count > 0 {
		if from >= s.count {
			return 0, false
		}
		s.emitted = from
	}
	return from, true
}

// civilDays counts calendar days from a's date to b's date, read in a's zone, so
// a DST change in between does not shorten the count.
func civilDays(a, b time.Time) int {
	b = b.In(a.Location())
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

// step walks i = from, from+1, ... feeding gen(i) into add until add stops
// (COUNT/UNTIL), the instance passes windowEnd, or limit steps are taken.
func (r Recurrence) step(from int, windowEnd time.Time, limit int, add func(time.Time) bool, gen func(int) time.Time) {
	for i := from; i < from+limit; i++ {
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
// instance, at seriesStart's clock time. Like everyDays it starts at the period
// just before the window, so limit bounds the periods walked inside the window
// rather than the years since DTSTART.
func (r Recurrence) weeklyByDay(sink *occurrenceSink, seriesStart, windowStart, windowEnd time.Time, interval, limit int) {
	want := wantedWeekdays(r.Weekdays)
	if len(want) == 0 {
		return
	}
	weekStart := weekStartMonday(seriesStart)
	from, ok := sink.skipWeeks(weekStart, seriesStart, windowStart, want, interval)
	if !ok {
		return
	}
	for period := from; period < from+limit; period++ {
		base := weekStart.AddDate(0, 0, 7*interval*period)
		if !weekInstances(base, seriesStart, want, sink.add) {
			return
		}
		if base.After(windowEnd) {
			return
		}
	}
}

// skipWeeks returns the first period worth generating for a weekly BYDAY rule: the
// one just before the window. The skipped periods' instances still count toward
// COUNT: the first period holds those on or after seriesStart, every later one all
// the listed weekdays. ok is false when COUNT is used up before the window begins.
func (s *occurrenceSink) skipWeeks(weekStart, seriesStart, windowStart time.Time, want map[time.Weekday]bool, interval int) (from int, ok bool) {
	from = max(civilDays(weekStart, windowStart)/(7*interval)-1, 0)
	if from == 0 || s.count == 0 {
		return from, true
	}
	first := 0
	weekInstances(weekStart, seriesStart, want, func(time.Time) bool { first++; return true })
	skipped := first + (from-1)*len(want)
	if skipped >= s.count {
		return 0, false
	}
	s.emitted = skipped
	return from, true
}

// wantedWeekdays resolves the BYDAY tokens to the weekdays they name.
func wantedWeekdays(tokens []string) map[time.Weekday]bool {
	want := map[time.Weekday]bool{}
	for _, d := range tokens {
		if wd, ok := weekdayToken(d); ok {
			want[wd] = true
		}
	}
	return want
}

// weekInstances feeds one week's matching days into add, at seriesStart's clock
// time. It reports false once the expansion must stop, either because the series
// ended or because the scan limit was reached.
func weekInstances(base, seriesStart time.Time, want map[time.Weekday]bool, add func(time.Time) bool) bool {
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
			return false
		}
	}
	return true
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
