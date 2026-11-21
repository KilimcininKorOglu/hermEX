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

// withRDate replaces the fixture's RDATE line.
func withRDate(line string) string {
	return strings.Replace(rdateSeriesICal, "RDATE:20260305T170000Z\r\n", line+"\r\n", 1)
}

// TestAddedPeriodKeepsItsOwnLength: RFC 5545 section 3.8.5.2 lets RDATE name a
// PERIOD, and section 3.3.9 gives it an explicit end or a duration, so the
// instance it adds occupies that period, not the series' one hour. The window
// holds the whole period, and the instance must end at the period's end.
func TestAddedPeriodKeepsItsOwnLength(t *testing.T) {
	for name, line := range map[string]string{
		"explicit end": "RDATE;VALUE=PERIOD:20260305T170000Z/20260305T190000Z",
		"duration":     "RDATE;VALUE=PERIOD:20260305T170000Z/PT2H",
		"zoned":        "RDATE;VALUE=PERIOD;TZID=Europe/Berlin:20260305T180000/20260305T200000",
	} {
		spans := spansIn(t, withRDate(line), day(5, 16), day(5, 20))
		if len(spans) != 1 || !spans[0].Start.Equal(day(5, 17)) || !spans[0].End.Equal(day(5, 19)) {
			t.Errorf("%s: spans = %v, want 17:00 to 19:00 on 5 March", name, spans)
		}
	}
}

// TestAddedPeriodExpandsAtItsOwnLength: the CalDAV expansion writes the period's
// end on the instance it adds.
func TestAddedPeriodExpandsAtItsOwnLength(t *testing.T) {
	out, ok := ExpandRecurrence([]byte(withRDate("RDATE;VALUE=PERIOD:20260305T170000Z/PT2H")), day(5, 0), day(6, 0))
	if !ok {
		t.Fatal("ExpandRecurrence refused the series")
	}
	if !strings.Contains(string(out), "DTEND:20260305T190000Z") {
		t.Errorf("expanded instance does not end at 19:00:\n%s", out)
	}
}

// TestUnusablePeriodAddsNothing: a value the RFC grammar does not allow adds no
// instance. A period must end after it starts, its halves must parse, a "/" value
// is a period only under VALUE=PERIOD (section 3.8.5.2), and a period is a
// date-time, which an all-day series has no place for.
func TestUnusablePeriodAddsNothing(t *testing.T) {
	cases := map[string]string{
		"end before start":  withRDate("RDATE;VALUE=PERIOD:20260305T190000Z/20260305T170000Z"),
		"zero duration":     withRDate("RDATE;VALUE=PERIOD:20260305T170000Z/PT0S"),
		"unparseable end":   withRDate("RDATE;VALUE=PERIOD:20260305T170000Z/tomorrow"),
		"missing end":       withRDate("RDATE;VALUE=PERIOD:20260305T170000Z"),
		"undeclared period": withRDate("RDATE:20260305T170000Z/20260305T190000Z"),
		"all-day series": strings.NewReplacer(
			"DTSTART:20260302T170000Z", "DTSTART;VALUE=DATE:20260302",
			"DTEND:20260302T180000Z", "DTEND;VALUE=DATE:20260303",
			"RDATE:20260305T170000Z", "RDATE;VALUE=PERIOD:20260305T170000Z/PT2H",
		).Replace(rdateSeriesICal),
	}
	for name, ical := range cases {
		if spans := spansIn(t, ical, day(5, 0), day(6, 0)); len(spans) != 0 {
			t.Errorf("%s: spans = %v, want none on 5 March", name, spans)
		}
	}
}
