package ews

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// seedSeries imports an iCalendar series into the mailbox's calendar the way a
// CalDAV PUT stores it.
func seedSeries(t *testing.T, path string, lines ...string) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msg, err := oxcical.Import([]byte(strings.Join(lines, "\r\n")+"\r\n"), oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), msg); err != nil {
		t.Fatal(err)
	}
}

// TestFreeBusyExpandsARecurringSeries proves a weekly meeting occupies the
// attendee's time in every week it recurs, not only at its first instance, and
// that an occurrence moved and marked tentative is reported with its own span,
// busy status and subject. Before the series was expanded, free/busy skipped a
// recurring master outright, so a weekly meeting never showed as busy.
func TestFreeBusyExpandsARecurringSeries(t *testing.T) {
	_, paths := availabilityServer(t)
	path := paths["bob@hermex.test"]
	seedSeries(t, path,
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN",
		"BEGIN:VEVENT", "UID:weekly-sync", "DTSTAMP:20260101T000000Z",
		"DTSTART:20260302T090000Z", "DTEND:20260302T100000Z",
		"RRULE:FREQ=WEEKLY;COUNT=10", "SUMMARY:Weekly sync", "TRANSP:OPAQUE",
		"END:VEVENT",
		"BEGIN:VEVENT", "UID:weekly-sync", "DTSTAMP:20260101T000000Z",
		"RECURRENCE-ID:20260323T090000Z",
		"DTSTART:20260323T140000Z", "DTEND:20260323T150000Z",
		"SUMMARY:Moved sync", "STATUS:TENTATIVE",
		"END:VEVENT",
		"END:VCALENDAR")

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The third and fourth weeks: one plain occurrence and the moved one.
	events, err := CalendarFreeBusy(st, time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC), true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CalendarEvent{}
	for _, ev := range events {
		got[ev.StartTime] = ev
	}
	if len(events) != 2 {
		t.Fatalf("the window reports %d events, want the two occurrences: %+v", len(events), events)
	}
	wantOccurrence(t, got, "2026-03-16T09:00:00Z", "Busy", "Weekly sync")
	wantOccurrence(t, got, "2026-03-23T14:00:00Z", "Tentative", "Moved sync")
	wantFlags(t, got["2026-03-16T09:00:00Z"], false)
	wantFlags(t, got["2026-03-23T14:00:00Z"], true)
}

// wantFlags checks an occurrence is marked recurring, and an exception only when
// an exception changed it.
func wantFlags(t *testing.T, ev CalendarEvent, exception bool) {
	t.Helper()
	if ev.Details == nil || !ev.Details.IsRecurring || ev.Details.IsException != exception {
		t.Errorf("occurrence %s details = %+v, want IsRecurring and IsException=%v", ev.StartTime, ev.Details, exception)
	}
}

// wantOccurrence checks the event reported at start carries the busy type and
// subject given.
func wantOccurrence(t *testing.T, got map[string]CalendarEvent, start, busy, subject string) {
	t.Helper()
	ev, ok := got[start]
	if !ok || ev.BusyType != busy || ev.Details == nil || ev.Details.Subject != subject {
		t.Errorf("the occurrence at %s = %+v, want %s %q", start, ev, busy, subject)
	}
}
