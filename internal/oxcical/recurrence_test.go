package oxcical

import (
	"reflect"
	"testing"
	"time"
)

// TestParseRecurrence covers the RRULE shapes ActiveSync needs: a weekly rule
// with an interval, weekday list, and count; a monthly nth-weekday rule with an
// until bound; and the rejection of a VEVENT that carries no RRULE.
func TestParseRecurrence(t *testing.T) {
	weekly := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T090000Z\r\n" +
		"DTEND:20260601T093000Z\r\nRRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR;COUNT=10\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")
	start, end, rec, ok := ParseRecurrence(weekly)
	if !ok {
		t.Fatal("ParseRecurrence failed on a valid recurring VEVENT")
	}
	wantTime(t, start, time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), "series start")
	wantTime(t, end, time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC), "series end")
	wantEq(t, rec.Freq, "WEEKLY", "frequency")
	wantEq(t, rec.Interval, 2, "interval")
	wantEq(t, rec.Count, 10, "count")
	wantEq(t, len(rec.Weekdays), 3, "weekday count")
	wantEq(t, rec.Weekdays[0], "MO", "first weekday")
	wantEq(t, rec.Weekdays[2], "FR", "last weekday")

	monthly := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T100000Z\r\n" +
		"RRULE:FREQ=MONTHLY;BYDAY=2TU;UNTIL=20261231T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	_, _, rec2, ok := ParseRecurrence(monthly)
	if !ok {
		t.Fatal("ParseRecurrence failed on the monthly rule")
	}
	wantEq(t, rec2.Freq, "MONTHLY", "monthly frequency")
	wantEq(t, rec2.SetPos, 2, "the nth-weekday ordinal")
	wantEq(t, len(rec2.Weekdays), 1, "monthly weekday count")
	wantEq(t, rec2.Weekdays[0], "TU", "monthly weekday")
	wantFalse(t, rec2.Until.IsZero(), "UNTIL is parsed")

	noRule := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	st, en, rec3, ok := ParseRecurrence(noRule)
	wantFalse(t, ok, "a VEVENT without RRULE parses as a recurrence")
	wantTrue(t, st.IsZero() && en.IsZero() && rec3.Freq == "", "refused ParseRecurrence returns zero values")
}

// TestParseRecurrenceRefusesMalformedInput covers malformed iCalendar, invalid
// DTSTART, and missing FREQ (RFC 5545 section 3.3.10).
func TestParseRecurrenceRefusesMalformedInput(t *testing.T) {
	for _, raw := range []string{
		"NOT-ICALENDAR",
		"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:not-a-date\r\nRRULE:FREQ=DAILY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T090000Z\r\nRRULE:INTERVAL=2;COUNT=5\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	} {
		st, en, rec, ok := ParseRecurrence([]byte(raw))
		if ok || !st.IsZero() || !en.IsZero() || rec.Freq != "" {
			t.Errorf("ParseRecurrence(%q) = (%v, %v, %+v, %v), want zero and false", raw, st, en, rec, ok)
		}
	}
}

// TestParseRRuleHostileCountsAndIntervals follows RFC 5545 3.3.10: INTERVAL and
// COUNT are positive integers and BYDAY names SU to SA, so a zero, negative,
// non-numeric or out-of-range value, or an unknown weekday, rejects the rule with
// nothing parsed. A valid but huge INTERVAL is accepted and must not overflow
// 7*interval or 12*interval in Occurrences.
func TestParseRRuleHostileCountsAndIntervals(t *testing.T) {
	for _, rule := range []string{
		"FREQ=DAILY;INTERVAL=-2", "FREQ=DAILY;INTERVAL=0", "FREQ=DAILY;COUNT=-5",
		"FREQ=DAILY;COUNT=99999999999999999999", "FREQ=DAILY;INTERVAL=x",
		"FREQ=WEEKLY;BYDAY=XX", "FREQ=DAILY;UNTIL=tomorrow", "FREQ=MONTHLY;BYMONTHDAY=x",
	} {
		if rec, ok := ParseRRule(rule); ok || !reflect.DeepEqual(rec, Recurrence{}) {
			t.Errorf("ParseRRule(%q) = (%+v, %v), want it refused with nothing parsed", rule, rec, ok)
		}
	}
	huge, ok := ParseRRule("FREQ=WEEKLY;INTERVAL=9223372036854775807")
	if !ok {
		t.Fatalf("ParseRRule with a huge positive INTERVAL was refused")
	}
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	if occ := huge.Occurrences(start, start, start.AddDate(1, 0, 0), 10); len(occ) != 0 {
		t.Errorf("oversized INTERVAL produced %d occurrences, want 0", len(occ))
	}
}
