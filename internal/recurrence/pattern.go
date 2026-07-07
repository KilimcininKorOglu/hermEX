// Package recurrence encodes and decodes the MS-OXOCAL RecurrencePattern binary
// structure (§2.2.1.44) that Outlook/MAPI store in PidLidAppointmentRecur
// (PSETID_Appointment/0x8216) for calendar items and PidLidTaskRecurrence
// (PSETID_Task/0x8416) for tasks. The structure carries the recurrence frequency,
// the interval, the end bound (a count, an until-instant, or open-ended), and the
// day/week/month pins. hermEX keeps recurrence as an RRULE string at the model layer
// (oxcical for calendar, oxtask.RecurrenceRule for tasks); this package is the wire
// bridge that renders the RRULE to and from the Outlook binary blob.
package recurrence

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PatternType values (MS-OXOCAL §2.2.1.44.1 PatternType).
const (
	PatternDay      uint16 = 0x0000 // daily, every N days
	PatternWeek     uint16 = 0x0001 // weekly, on a set of weekdays
	PatternMonth    uint16 = 0x0002 // monthly, day N of the month (MonthEnd in the spec name)
	PatternMonthNth uint16 = 0x0003 // monthly, the Nth weekday of the month
)

// RecurFrequency values (MS-OXOCAL §2.2.1.44.1 RecurFrequency).
const (
	FreqDaily   uint16 = 0x200A
	FreqWeekly  uint16 = 0x200B
	FreqMonthly uint16 = 0x200C
	FreqYearly  uint16 = 0x200D
)

// EndType values (MS-OXOCAL §2.2.1.44.1 EndType).
const (
	EndAfterDate uint32 = 0x00002021
	EndAfterN    uint32 = 0x00002022
	EndNever     uint32 = 0x00002023
	EndNeverAlt  uint32 = 0xFFFFFFFF
)

// noEndDate is the sentinel EndDate Outlook writes for an open-ended series.
const noEndDate uint32 = 0x5AE980DF

// epoch1601 is the FILETIME epoch (1601-01-01 00:00 UTC), the reference RecurrencePattern
// dates count minutes from.
var epoch1601 = time.Date(1601, 1, 1, 0, 0, 0, 0, time.UTC)

// Pattern is the decoded RecurrencePattern (the §2.2.1.44.1 fields hermEX uses; the
// exception blocks and TimeZone are not modeled here yet, v1 carries UTC instants
// and zero exceptions). StartDate is the series start, EndDate the last occurrence
// (zero for an open-ended series).
type Pattern struct {
	RecurFrequency  uint16
	PatternType     uint16
	FirstDateTime   uint32 // minutes from 1601 to the first day/week/month of the series
	Period          uint32 // interval (minutes for daily, weeks for weekly, months for monthly/yearly)
	EndType         uint32
	OccurrenceCount uint32
	FirstDOW        uint32 // 0 Sunday .. 6 Saturday
	DayOfWeek       uint32 // weekly/monthly-nth bitmask (Sunday 1 .. Saturday 64)
	DayOfMonth      uint32 // monthly day-N
	WeekOfMonth     uint32 // monthly-nth week (1..5, 5 = last)
	StartDate       uint32 // minutes from 1601 to first occurrence
	EndDate         uint32 // minutes from 1601 to last occurrence, or noEndDate
}

// rrule is the parsed RRULE shape the binary encoder needs. It mirrors the neutral
// recurrence shape oxcical carries, kept local here to avoid the oxcical<->oxtask
// import cycle (oxcical/vtodo imports oxtask). A change to the RRULE grammar is a
// change to oxcical.parseRRule first, then mirrored here.
type rrule struct {
	Freq     string
	Interval int
	Count    int
	Until    time.Time
	Weekdays []string
	SetPos   int
	MonthDay int
	Month    int
}

// parseRRule parses an RRULE value (the text after "RRULE:") into the local rrule. A
// value with no FREQ is rejected.
func parseRRule(value string) (rrule, bool) {
	r := rrule{Interval: 1}
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
		return rrule{}, false
	}
	return r, true
}

// parseByDay splits a BYDAY value into its weekday tokens, returning any single
// ordinal prefix it carries.
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

// FromRRule renders the RRULE a PIM object stores to the RecurrencePattern binary
// layout, anchored at seriesStart (the master's DTSTART). It covers the four base
// frequencies with end-by-date (UNTIL), end-after-N (COUNT), and never-end ranges;
// the nth-weekday monthly/yearly shapes carry WeekOfMonth + DayOfWeek. It does not
// emit exceptions and uses a no-DST TimeZone.
func FromRRule(rruleText string, seriesStart time.Time) ([]byte, error) {
	rec, ok := parseRRule(rruleText)
	if !ok {
		return nil, errors.New("recurrence: invalid RRULE")
	}
	p, err := patternFromRecurrence(rec, seriesStart)
	if err != nil {
		return nil, err
	}
	return p.MarshalBinary(), nil
}

// patternFromRecurrence maps the parsed RRULE to a Pattern.
func patternFromRecurrence(r rrule, start time.Time) (Pattern, error) {
	p := Pattern{
		FirstDOW:  0, // Sunday
		StartDate: minutesSince1601(start),
	}
	switch r.Freq {
	case "DAILY":
		p.RecurFrequency = FreqDaily
		p.PatternType = PatternDay
		periodMinutes := uint32(max(r.Interval, 1)) * 24 * 60
		p.Period = periodMinutes
		p.FirstDateTime = p.StartDate % periodMinutes
	case "WEEKLY":
		p.RecurFrequency = FreqWeekly
		p.PatternType = PatternWeek
		p.Period = uint32(max(r.Interval, 1)) // weeks
		p.DayOfWeek = uint32(weekdayBitmask(r.Weekdays))
		if p.DayOfWeek == 0 {
			p.DayOfWeek = 1 << uint(start.Weekday()) // default to the start weekday
		}
		p.FirstDateTime = weeklyFirstDateTime(start, p.Period)
	case "MONTHLY":
		p.RecurFrequency = FreqMonthly
		if len(r.Weekdays) > 0 {
			p.PatternType = PatternMonthNth
			p.DayOfWeek = uint32(weekdayBitmask(r.Weekdays))
			p.WeekOfMonth = weekOfMonthFromSetPos(r.SetPos)
		} else {
			p.PatternType = PatternMonth
			if r.MonthDay != 0 {
				p.DayOfMonth = uint32(r.MonthDay)
			} else {
				p.DayOfMonth = uint32(start.Day())
			}
		}
		p.Period = uint32(max(r.Interval, 1)) // months
		p.FirstDateTime = monthlyFirstDateTime(start, p.Period)
	case "YEARLY":
		p.RecurFrequency = FreqYearly
		if len(r.Weekdays) > 0 {
			p.PatternType = PatternMonthNth
			p.DayOfWeek = uint32(weekdayBitmask(r.Weekdays))
			p.WeekOfMonth = weekOfMonthFromSetPos(r.SetPos)
		} else {
			p.PatternType = PatternMonth
			p.DayOfMonth = uint32(start.Day())
		}
		p.Period = uint32(max(r.Interval, 1)) * 12 // months per interval
		p.FirstDateTime = monthlyFirstDateTime(start, p.Period)
	default:
		return Pattern{}, fmt.Errorf("recurrence: unsupported FREQ %q", r.Freq)
	}

	switch {
	case !r.Until.IsZero():
		p.EndType = EndAfterDate
		p.EndDate = minutesSince1601(r.Until)
		p.OccurrenceCount = 0xA // computed; Outlook tolerates the placeholder for end-by-date
	case r.Count > 0:
		p.EndType = EndAfterN
		p.OccurrenceCount = uint32(r.Count)
		p.EndDate = noEndDate
	default:
		p.EndType = EndNever
		p.OccurrenceCount = 0xA
		p.EndDate = noEndDate
	}
	return p, nil
}

// MarshalBinary encodes the Pattern as the MS-OXOCAL RecurrencePattern bytes
// (the leading fixed fields; no TimeZone block, no exceptions for v1).
func (p Pattern) MarshalBinary() []byte {
	var b []byte
	put16 := func(v uint16) { b = binary.LittleEndian.AppendUint16(b, v) }
	put32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	put16(0x3004) // ReaderVersion
	put16(0x3004) // WriterVersion
	put16(p.RecurFrequency)
	put16(p.PatternType)
	put16(0x0000) // CalendarType (Gregorian)
	put32(p.FirstDateTime)
	put32(p.Period)
	put32(0) // SlidingFlag
	switch p.PatternType {
	case PatternDay:
		put32(0) // PatternTypeSpecific unused for daily
	case PatternWeek:
		put32(p.DayOfWeek)
	case PatternMonth:
		put32(p.DayOfMonth)
	case PatternMonthNth:
		put32(p.DayOfWeek)
		put32(p.WeekOfMonth)
	default:
		put32(0)
	}
	put32(p.EndType)
	put32(p.OccurrenceCount)
	put32(p.FirstDOW)
	put32(0) // DeletedInstanceCount
	put32(0) // ModifiedInstanceCount
	put32(p.StartDate)
	put32(p.EndDate)
	return b
}

// minutesSince1601 returns the minutes from the FILETIME epoch to t (UTC).
func minutesSince1601(t time.Time) uint32 {
	if t.Before(epoch1601) {
		return 0
	}
	return uint32(t.Sub(epoch1601) / time.Minute)
}

// weeklyFirstDateTime computes the minutes from 1601 to the first day of the week
// containing start (using Sunday as the week start), modulo the period in minutes.
func weeklyFirstDateTime(start time.Time, periodWeeks uint32) uint32 {
	daysSinceSunday := uint32(start.Weekday())
	weekStart := start.AddDate(0, 0, -int(daysSinceSunday))
	weekStart = time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, time.UTC)
	periodMinutes := periodWeeks * 7 * 24 * 60
	if periodMinutes == 0 {
		return 0
	}
	return minutesSince1601(weekStart) % periodMinutes
}

// monthlyFirstDateTime computes the months from 1601-01 to start's month, modulo
// the period in months.
func monthlyFirstDateTime(start time.Time, periodMonths uint32) uint32 {
	monthsSince1601 := uint32(start.Year()-1601)*12 + uint32(start.Month()-1)
	if periodMonths == 0 {
		return 0
	}
	return monthsSince1601 % periodMonths
}

// weekOfMonthFromSetPos maps an RRULE ordinal (-1 last, 1..5 nth) to the MS-OXOCAL
// WeekOfMonth (1..5, 5 = last); 0 means unset.
func weekOfMonthFromSetPos(setPos int) uint32 {
	switch {
	case setPos == -1:
		return 5
	case setPos >= 1 && setPos <= 5:
		return uint32(setPos)
	}
	return 0
}

// weekdayBitmask folds BYDAY weekday tokens into the MS-ASCAL/MS-OXOCAL DayOfWeek
// bitmask (Sunday 1, Monday 2, ... Saturday 64).
func weekdayBitmask(days []string) int {
	bits := map[string]int{"SU": 1, "MO": 2, "TU": 4, "WE": 8, "TH": 16, "FR": 32, "SA": 64}
	mask := 0
	for _, d := range days {
		mask |= bits[d]
	}
	return mask
}
