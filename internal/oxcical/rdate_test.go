package oxcical

import (
	"strings"
	"testing"
	"time"
)

// rdateSeriesICal is the weekly series with one extra date added by RDATE. The
// added date (5 March) falls between the second and third rule instances.
const rdateSeriesICal = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:series-1\r\n" +
	"SUMMARY:Weekly\r\n" +
	"DTSTART:20260302T170000Z\r\n" +
	"DTEND:20260302T180000Z\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=6\r\n" +
	"RDATE:20260305T170000Z\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// rdateOnlyICal defines its whole recurrence set with RDATE and carries no RRULE.
const rdateOnlyICal = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:rdate-only-1\r\n" +
	"SUMMARY:Irregular\r\n" +
	"DTSTART:20260302T170000Z\r\n" +
	"DTEND:20260302T180000Z\r\n" +
	"RDATE:20260305T170000Z,20260311T170000Z\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// TestOccurrencesInCountsAnAddedDate pins that a conflict check sees an RDATE
// instance. RFC 5545 section 3.8.5 makes RDATE and RRULE two halves of one
// recurrence set, so a date the rule never produces still occupies its slot.
func TestOccurrencesInCountsAnAddedDate(t *testing.T) {
	spans := spansIn(t, rdateSeriesICal, day(5, 16), day(5, 20))
	if len(spans) != 1 {
		t.Fatalf("spans = %v, want the added 5 March instance", spans)
	}
	if !spans[0].Start.Equal(day(5, 17)) || !spans[0].End.Equal(day(5, 18)) {
		t.Errorf("span = %v, want 17:00 to 18:00", spans[0])
	}
}

// TestOccurrencesInExpandsAnRdateOnlySeries proves an object whose recurrence set
// is RDATE alone is a series: without this the caller falls back to the stored
// start and end and sees only the first instance.
func TestOccurrencesInExpandsAnRdateOnlySeries(t *testing.T) {
	spans := spansIn(t, rdateOnlyICal, day(1, 0), day(31, 0))
	if len(spans) != 3 {
		t.Fatalf("spans = %v, want the start plus both added dates", spans)
	}
	want := []time.Time{day(2, 17), day(5, 17), day(11, 17)}
	for i, w := range want {
		if !spans[i].Start.Equal(w) {
			t.Errorf("span %d starts %v, want %v", i, spans[i].Start, w)
		}
	}
}

// TestExcludedDateBeatsAnAddedDate pins the precedence RFC 5545 section 3.8.5.1
// states: EXDATE removes an instance from the recurrence set, whichever half put
// it there.
func TestExcludedDateBeatsAnAddedDate(t *testing.T) {
	ical := strings.Replace(rdateSeriesICal,
		"RDATE:20260305T170000Z\r\n",
		"RDATE:20260305T170000Z\r\nEXDATE:20260305T170000Z\r\n", 1)
	if spans := spansIn(t, ical, day(5, 16), day(5, 20)); len(spans) != 0 {
		t.Errorf("spans = %v, want none for an excluded added date", spans)
	}
}

// TestExpandRecurrenceEmitsAnAddedDate is the CalDAV expand report's half of the
// same guarantee: the added instance is one of the emitted VEVENTs, in
// chronological order with the rule's own instances.
func TestExpandRecurrenceEmitsAnAddedDate(t *testing.T) {
	out, ok := ExpandRecurrence([]byte(rdateSeriesICal), day(1, 0), day(10, 0))
	if !ok {
		t.Fatal("ExpandRecurrence refused the series")
	}
	got := string(out)
	if !strings.Contains(got, "RECURRENCE-ID:20260305T170000Z") {
		t.Errorf("expansion carries no added instance:\n%s", got)
	}
	second := strings.Index(got, "RECURRENCE-ID:20260302T170000Z")
	added := strings.Index(got, "RECURRENCE-ID:20260305T170000Z")
	third := strings.Index(got, "RECURRENCE-ID:20260309T170000Z")
	if second < 0 || third < 0 || second >= added || added >= third {
		t.Errorf("added instance is out of order (2 Mar at %d, 5 Mar at %d, 9 Mar at %d)", second, added, third)
	}
}

// TestExpandRecurrenceRefusesAPlainEvent keeps the contract a non-recurring
// object relies on: an event with neither RRULE nor RDATE is not a series, so the
// caller serves it unchanged.
func TestExpandRecurrenceRefusesAPlainEvent(t *testing.T) {
	plain := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nUID:plain-1\r\nDTSTART:20260302T170000Z\r\n" +
		"DTEND:20260302T180000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if _, ok := ExpandRecurrence([]byte(plain), day(1, 0), day(31, 0)); ok {
		t.Error("ExpandRecurrence expanded an event that defines no recurrence set")
	}
	if _, ok := OccurrencesIn([]byte(plain), day(1, 0), day(31, 0)); ok {
		t.Error("OccurrencesIn expanded an event that defines no recurrence set")
	}
}

// TestAddedPeriodIsNotRead states the measured limit: an RDATE PERIOD value
// carries a duration of its own, which an expansion built on the series duration
// cannot honor, so it adds no instance.
func TestAddedPeriodIsNotRead(t *testing.T) {
	ical := strings.Replace(rdateSeriesICal,
		"RDATE:20260305T170000Z\r\n",
		"RDATE;VALUE=PERIOD:20260305T170000Z/20260305T190000Z\r\n", 1)
	if spans := spansIn(t, ical, day(5, 16), day(5, 20)); len(spans) != 0 {
		t.Errorf("spans = %v, want none: a PERIOD value is not read", spans)
	}
}
