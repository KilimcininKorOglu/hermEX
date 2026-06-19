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
	if !start.Equal(time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", start)
	}
	if !end.Equal(time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("end = %v", end)
	}
	if rec.Freq != "WEEKLY" || rec.Interval != 2 || rec.Count != 10 {
		t.Errorf("rec = %+v", rec)
	}
	if len(rec.Weekdays) != 3 || rec.Weekdays[0] != "MO" || rec.Weekdays[2] != "FR" {
		t.Errorf("weekdays = %v", rec.Weekdays)
	}

	monthly := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T100000Z\r\n" +
		"RRULE:FREQ=MONTHLY;BYDAY=2TU;UNTIL=20261231T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	_, _, rec2, ok := ParseRecurrence(monthly)
	if !ok {
		t.Fatal("ParseRecurrence failed on the monthly rule")
	}
	if rec2.Freq != "MONTHLY" || rec2.SetPos != 2 || len(rec2.Weekdays) != 1 || rec2.Weekdays[0] != "TU" {
		t.Errorf("monthly rec = %+v", rec2)
	}
	if rec2.Until.IsZero() {
		t.Error("UNTIL was not parsed")
	}

	noRule := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260601T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, _, _, ok := ParseRecurrence(noRule); ok {
		t.Error("a VEVENT without RRULE should not parse as a recurrence")
	}
}
