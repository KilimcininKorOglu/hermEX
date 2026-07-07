package recurrence

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// TestFromRRuleDaily verifies the daily RRULE renders the MS-OXOCAL RecurrencePattern
// header fields Outlook reads: Reader/WriterVersion 0x3004, RecurFrequency 0x200A,
// PatternType Day, the interval as Period in minutes, StartDate as minutes since
// 1601, and EndNever for an open-ended rule.
func TestFromRRuleDaily(t *testing.T) {
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC) // a Monday
	b, err := FromRRule("FREQ=DAILY;INTERVAL=3", start)
	if err != nil {
		t.Fatal(err)
	}
	if rv := binary.LittleEndian.Uint16(b[0:2]); rv != 0x3004 {
		t.Errorf("ReaderVersion = %#x, want 0x3004", rv)
	}
	if wv := binary.LittleEndian.Uint16(b[2:4]); wv != 0x3004 {
		t.Errorf("WriterVersion = %#x, want 0x3004", wv)
	}
	if got := binary.LittleEndian.Uint16(b[4:6]); got != FreqDaily {
		t.Errorf("RecurFrequency = %#x, want %#x", got, FreqDaily)
	}
	if got := binary.LittleEndian.Uint16(b[6:8]); got != PatternDay {
		t.Errorf("PatternType = %#x, want %#x", got, PatternDay)
	}
	if got := binary.LittleEndian.Uint32(b[14:18]); got != 3*24*60 {
		t.Errorf("Period = %d, want %d (3 days in minutes)", got, 3*24*60)
	}
	// Daily layout: header(10) + FirstDateTime(4) + Period(4) + Sliding(4) + PTS(4) + EndType(4) + ...
	if got := binary.LittleEndian.Uint32(b[26:30]); got != EndNever {
		t.Errorf("EndType = %#x, want EndNever", got)
	}
	if got := binary.LittleEndian.Uint32(b[50:54]); got != noEndDate {
		t.Errorf("EndDate = %#x, want noEndDate", got)
	}
	if got := binary.LittleEndian.Uint32(b[46:50]); got != minutesSince1601(start) {
		t.Errorf("StartDate = %d, want %d", got, minutesSince1601(start))
	}
}

// TestFromRRuleWeeklyCount verifies a weekly rule with BYDAY and COUNT pins DayOfWeek
// and EndAfterN with the count, the shapes a device authored task/calendar reaches
// Outlook with.
func TestFromRRuleWeeklyCount(t *testing.T) {
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC) // Monday
	b, err := FromRRule("FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO,WE,FR", start)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(b[6:8]); got != PatternWeek {
		t.Errorf("PatternType = %#x, want PatternWeek", got)
	}
	if got := binary.LittleEndian.Uint16(b[4:6]); got != FreqWeekly {
		t.Errorf("RecurFrequency = %#x, want FreqWeekly", got)
	}
	// Weekly layout: ... + Sliding(4 at 18) + DayOfWeek(4 at 22) + EndType(4 at 26) + OccCount(4 at 30).
	if got := binary.LittleEndian.Uint32(b[22:26]); got != 2|8|32 {
		t.Errorf("DayOfWeek bitmask = %d, want 42 (Mon|Wed|Fri)", got)
	}
	if got := binary.LittleEndian.Uint32(b[26:30]); got != EndAfterN {
		t.Errorf("EndType = %#x, want EndAfterN", got)
	}
	if got := binary.LittleEndian.Uint32(b[30:34]); got != 5 {
		t.Errorf("OccurrenceCount = %d, want 5", got)
	}
}

// TestFromRRuleMonthlyNth verifies the nth-weekday monthly rule writes WeekOfMonth
// and DayOfWeek into the PatternTypeSpecific block.
func TestFromRRuleMonthlyNth(t *testing.T) {
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC) // first Monday of July 2026
	// First Monday of every month: BYDAY=MO with setPos 1.
	b, err := FromRRule("FREQ=MONTHLY;INTERVAL=1;BYDAY=MO;BYSETPOS=1", start)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(b[6:8]); got != PatternMonthNth {
		t.Errorf("PatternType = %#x, want PatternMonthNth", got)
	}
	// MonthNth layout: ... + Sliding(4 at 18) + DayOfWeek(4 at 22) + WeekOfMonth(4 at 26) + EndType(4 at 30).
	if got := binary.LittleEndian.Uint32(b[22:26]); got != 2 {
		t.Errorf("DayOfWeek = %d, want 2 (Monday)", got)
	}
	if got := binary.LittleEndian.Uint32(b[26:30]); got != 1 {
		t.Errorf("WeekOfMonth = %d, want 1 (first)", got)
	}
}

// TestFromRRuleUntil verifies an UNTIL-bound rule pins EndAfterDate with the until
// instant's minutes-since-1601.
func TestFromRRuleUntil(t *testing.T) {
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)
	b, err := FromRRule("FREQ=DAILY;UNTIL=20261231T235900Z", start)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(b[26:30]); got != EndAfterDate {
		t.Errorf("EndType = %#x, want EndAfterDate", got)
	}
	if got := binary.LittleEndian.Uint32(b[50:54]); got != minutesSince1601(until) {
		t.Errorf("EndDate = %d, want minutes-since-1601 of the UNTIL", got)
	}
}

// TestFromRRuleInvalid rejects an empty / malformed RRULE so a bad store value never
// produces a misleading Outlook blob.
func TestFromRRuleInvalid(t *testing.T) {
	if _, err := FromRRule("", time.Now()); err == nil {
		t.Error("empty RRULE should error")
	}
	if _, err := FromRRule("FREQ=BOGUS", time.Now()); err == nil {
		t.Error("unknown FREQ should error")
	}
}

// TestBlobRoundTrip proves a recurrence Outlook wrote as the MS-OXOCAL binary blob
// decodes back to the same RRULE shape (FREQ, INTERVAL, BYDAY, end range) so the
// EAS/webmail paths read a MAPI-authored series instead of dropping it.
func TestBlobRoundTrip(t *testing.T) {
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC) // Monday
	cases := []struct {
		name  string
		rrule string
	}{
		{"daily-never", "FREQ=DAILY;INTERVAL=3"},
		{"weekly-count", "FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO,WE,FR"},
		{"weekly-until", "FREQ=WEEKLY;INTERVAL=2;UNTIL=20261231T235900Z;BYDAY=MO"},
		{"monthly-day", "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=15"},
		{"monthly-nth", "FREQ=MONTHLY;INTERVAL=1;BYDAY=MO;BYSETPOS=1"},
		{"yearly", "FREQ=YEARLY;INTERVAL=1;BYMONTHDAY=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := FromRRule(tc.rrule, start)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, ok := ToRRule(blob)
			if !ok {
				t.Fatalf("decode failed for %q", tc.rrule)
			}
			if !rruleEqual(got, tc.rrule) {
				t.Errorf("round-trip = %q, want %q", got, tc.rrule)
			}
		})
	}
}

// rruleEqual compares two RRULEs by their sorted semicolon parts, so a difference in
// clause order (the encoder's choice) does not fail an otherwise-equal rule.
func rruleEqual(a, b string) bool {
	pa := splitParts(a)
	pb := splitParts(b)
	if len(pa) != len(pb) {
		return false
	}
	for k := range pa {
		if pa[k] != pb[k] {
			return false
		}
	}
	return true
}

func splitParts(s string) map[string]string {
	out := map[string]string{}
	for part := range strings.SplitSeq(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.ToUpper(k)] = v
	}
	return out
}
