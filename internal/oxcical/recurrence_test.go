package oxcical

import (
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
	_, _, _, ok = ParseRecurrence(noRule)
	wantFalse(t, ok, "a VEVENT without RRULE parses as a recurrence")
}
