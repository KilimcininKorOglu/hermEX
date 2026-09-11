package oxcical

import (
	"testing"
	"time"
)

// A recurring VEVENT is required to carry DTSTART, and a stored object may still
// arrive without one: a CalDAV client PUTs the bytes it chooses, and an inbound
// meeting message carries whatever the sending system wrote. Every function that
// reads a start time from such an object must answer, not fail.
const seriesWithoutDTSTART = "BEGIN:VCALENDAR\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:no-dtstart\r\n" +
	"RRULE:FREQ=DAILY;COUNT=3\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// An override names the instance it replaces with RECURRENCE-ID and may leave
// DTSTART out, which means "same time as the generated instance".
const overrideWithoutDTSTART = "BEGIN:VCALENDAR\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:override-no-dtstart\r\n" +
	"DTSTART:20260101T090000Z\r\n" +
	"DTEND:20260101T100000Z\r\n" +
	"RRULE:FREQ=DAILY;COUNT=3\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:override-no-dtstart\r\n" +
	"RECURRENCE-ID:20260102T090000Z\r\n" +
	"SUMMARY:moved\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

var testWindowStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ExpandRecurrence serves the CalDAV calendar-query REPORT with expand. A series
// it cannot place must be reported as unexpandable, so the caller returns the
// object as stored.
func TestExpandRecurrenceRefusesASeriesWithoutAStart(t *testing.T) {
	out, ok := ExpandRecurrence([]byte(seriesWithoutDTSTART), testWindowStart, testWindowStart.AddDate(0, 0, 7))
	if ok {
		t.Fatalf("ExpandRecurrence reported success for a series with no DTSTART, output %q", out)
	}
}

// OccurrencesIn answers the free/busy and conflict questions. A series with no
// start is not a readable series, so the caller falls back to the item's own
// stored span.
func TestOccurrencesInRefusesASeriesWithoutAStart(t *testing.T) {
	spans, ok := OccurrencesIn([]byte(seriesWithoutDTSTART), testWindowStart, testWindowStart.AddDate(0, 0, 7))
	if ok {
		t.Fatalf("OccurrencesIn reported a series for an object with no DTSTART, spans %v", spans)
	}
}

// An override with no DTSTART keeps the instant its RECURRENCE-ID names, and the
// series duration.
func TestOccurrencesInPlacesAnOverrideWithoutAStart(t *testing.T) {
	spans, ok := OccurrencesIn([]byte(overrideWithoutDTSTART), testWindowStart, testWindowStart.AddDate(0, 0, 7))
	if !ok {
		t.Fatal("OccurrencesIn refused an object whose master carries DTSTART")
	}
	want := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	found := false
	for _, s := range spans {
		if s.Start.Equal(want) && s.End.Equal(want.Add(time.Hour)) {
			found = true
		}
	}
	if !found {
		t.Errorf("spans = %v, want one at %s lasting the series hour", spans, want.Format(time.RFC3339))
	}
}

// CancelOccurrence adds an EXDATE when an inbound CANCEL drops one instance. A
// master with no DTSTART still gets its EXDATE.
func TestCancelOccurrenceHandlesASeriesWithoutAStart(t *testing.T) {
	out, ok := CancelOccurrence([]byte(seriesWithoutDTSTART), testWindowStart)
	if !ok {
		t.Fatal("CancelOccurrence refused a series with no DTSTART")
	}
	if len(out) == 0 {
		t.Error("CancelOccurrence returned an empty object")
	}
}
