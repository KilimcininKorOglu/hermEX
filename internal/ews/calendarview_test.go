package ews

import (
	"strings"
	"testing"
)

// calendarViewReq is a FindItem over the calendar through a CalendarView.
func calendarViewReq(start, end, maxEntries string) string {
	view := `<CalendarView StartDate="` + start + `" EndDate="` + end + `"`
	if maxEntries != "" {
		view += ` MaxEntriesReturned="` + maxEntries + `"`
	}
	return wrapRequest(`<FindItem Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>AllProperties</BaseShape></ItemShape>` + view + `/>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="calendar" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindItem>`)
}

// occurrenceItemReq is a GetItem naming a series occurrence by master and index.
func occurrenceItemReq(masterID, index string) string {
	return wrapRequest(`<GetItem xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>AllProperties</BaseShape></ItemShape>` +
		`<ItemIds><t:OccurrenceItemId RecurringMasterId="` + masterID + `" InstanceIndex="` + index + `" xmlns:t="` + nsTypes + `"/></ItemIds>` +
		`</GetItem>`)
}

// seedWeeklySeries stores a weekly series of four with its second week moved.
func seedWeeklySeries(t *testing.T, dir string) {
	t.Helper()
	importAppointment(t, dir,
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN",
		"BEGIN:VEVENT", "UID:view-weekly", "DTSTAMP:20260101T000000Z",
		"DTSTART:20261005T090000Z", "DTEND:20261005T100000Z",
		"RRULE:FREQ=WEEKLY;COUNT=4", "SUMMARY:Weekly", "LOCATION:Room 1",
		"END:VEVENT",
		"BEGIN:VEVENT", "UID:view-weekly", "DTSTAMP:20260101T000000Z",
		"RECURRENCE-ID:20261012T090000Z",
		"DTSTART:20261012T130000Z", "DTEND:20261012T140000Z", "SUMMARY:Moved",
		"END:VEVENT",
		"END:VCALENDAR")
}

// TestCalendarViewExpandsASeries proves a CalendarView lists a series as its
// occurrences in the window, each typed and carrying the instant it was generated
// for, with the moved one at its own time. A calendar listed without a view
// showed one master for the whole series, so a client drew only its first week.
func TestCalendarViewExpandsASeries(t *testing.T) {
	ts, dir := seededWithMessage(t)
	seedWeeklySeries(t, dir)
	importAppointment(t, dir,
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN",
		"BEGIN:VEVENT", "UID:view-single", "DTSTAMP:20260101T000000Z",
		"DTSTART:20261020T080000Z", "DTEND:20261020T083000Z", "SUMMARY:Single",
		"END:VEVENT", "END:VCALENDAR")

	_, out := soapPost(t, ts, calendarViewReq("2026-10-01T00:00:00Z", "2026-10-22T00:00:00Z", ""), true)
	if n := strings.Count(out, "<CalendarItem "); n != 4 {
		t.Fatalf("the view lists %d items, want three occurrences and the single: %s", n, out)
	}
	for _, want := range []string{
		`TotalItemsInView="4"`,
		"<Start>2026-10-12T13:00:00Z</Start><End>2026-10-12T14:00:00Z</End><OriginalStart>2026-10-12T09:00:00Z</OriginalStart>",
		"<Subject>Moved</Subject>",
		"<CalendarItemType>Exception</CalendarItemType>",
		"<Start>2026-10-19T09:00:00Z</Start>",
		"<CalendarItemType>Single</CalendarItemType>",
	} {
		wantContains(t, "the calendar view", out, want)
	}
	if n := strings.Count(out, "<CalendarItemType>Occurrence</CalendarItemType>"); n != 2 {
		t.Errorf("the view types %d items as occurrences, want 2: %s", n, out)
	}
	if strings.Contains(out, "RecurringMaster") {
		t.Errorf("the view lists the series master: %s", out)
	}
}

// TestCalendarViewHonoursMaxEntries proves the view stops at MaxEntriesReturned
// and says the range goes on, and refuses a window that ends before it starts.
func TestCalendarViewHonoursMaxEntries(t *testing.T) {
	ts, dir := seededWithMessage(t)
	seedWeeklySeries(t, dir)

	_, out := soapPost(t, ts, calendarViewReq("2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "2"), true)
	if n := strings.Count(out, "<CalendarItem "); n != 2 {
		t.Fatalf("the view lists %d items, want 2: %s", n, out)
	}
	wantContains(t, "the truncated view", out, `IncludesLastItemInRange="false"`)
	wantContains(t, "the first entry", out, "<Start>2026-10-05T09:00:00Z</Start>")

	_, out = soapPost(t, ts, calendarViewReq("2026-11-01T00:00:00Z", "2026-10-01T00:00:00Z", ""), true)
	wantContains(t, "a reversed window", out, "ErrorCalendarEndDateIsEarlierThanStartDate")
	_, out = soapPost(t, ts, calendarViewReq("2026-01-01T00:00:00Z", "2029-01-01T00:00:00Z", ""), true)
	wantContains(t, "a window over two years", out, "ErrorCalendarViewRangeTooBig")
}

// TestGetItemReadsAnOccurrence proves an occurrence is read back by the id the
// view gave it and by its master and index, with the exception's own details, and
// that an index past the series is refused.
func TestGetItemReadsAnOccurrence(t *testing.T) {
	ts, dir := seededWithMessage(t)
	seedWeeklySeries(t, dir)

	_, out := soapPost(t, ts, calendarViewReq("2026-10-10T00:00:00Z", "2026-10-13T00:00:00Z", ""), true)
	ids := itemIDRE.FindStringSubmatch(out)
	if len(ids) != 2 {
		t.Fatalf("the view returned no ItemId: %s", out)
	}
	_, got := soapPost(t, ts, getItemReq(ids[1]), true)
	wantContains(t, "the occurrence by its id", got, "<Start>2026-10-12T13:00:00Z</Start>")
	wantContains(t, "the occurrence subject", got, "<Subject>Moved</Subject>")

	_, out = soapPost(t, ts, findItemReq("calendar"), true)
	master := itemIDRE.FindStringSubmatch(out)
	if len(master) != 2 {
		t.Fatalf("FindItem returned no master ItemId: %s", out)
	}
	_, got = soapPost(t, ts, occurrenceItemReq(master[1], "3"), true)
	wantContains(t, "the third occurrence", got, "<Start>2026-10-19T09:00:00Z</Start>")
	wantContains(t, "the third occurrence type", got, "<CalendarItemType>Occurrence</CalendarItemType>")
	_, got = soapPost(t, ts, occurrenceItemReq(master[1], "5"), true)
	wantContains(t, "an index past the series", got, "ErrorCalendarOccurrenceIndexIsOutOfRecurrenceRange")
}
