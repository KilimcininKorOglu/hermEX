package oxcical

import (
	"testing"
	"time"

	"hermex/internal/oxtask"
)

// TestVTODORoundTrip proves a task survives export to a VTODO and parse back, so a
// CalDAV tasks client and the other protocols share one task.
func TestVTODORoundTrip(t *testing.T) {
	in := oxtask.Task{
		Subject:       "Ship release",
		Body:          "cut the tag",
		Start:         time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC),
		Due:           time.Date(2026, 7, 1, 17, 0, 0, 0, time.UTC),
		Complete:      true,
		DateCompleted: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		Importance:    2,
		Sensitivity:   -1,
		Categories:    []string{"Work", "Urgent"},
	}
	ics := ExportVTODO(in, "task-1@hermex.test", time.Time{})
	s := string(ics)
	wantContains(t, s, "BEGIN:VTODO", "the export opens a VTODO")
	wantContains(t, s, "SUMMARY:Ship release", "the export carries the subject")
	wantContains(t, s, "STATUS:COMPLETED", "the export carries the completion status")
	wantContains(t, s, "DUE:20260701T170000Z", "the export carries the due date")

	out, uid, ok := ParseVTODO(ics)
	if !ok {
		t.Fatal("ParseVTODO returned ok=false")
	}
	wantEq(t, uid, "task-1@hermex.test", "parsed uid")
	wantEq(t, out.Subject, in.Subject, "parsed subject")
	wantEq(t, out.Body, in.Body, "parsed body")
	wantTime(t, out.Start, in.Start, "parsed start")
	wantTime(t, out.Due, in.Due, "parsed due")
	wantTrue(t, out.Complete, "the task parses back as complete")
	wantEq(t, out.Importance, 2, "parsed importance")
	wantEq(t, len(out.Categories), 2, "parsed category count")
	wantEq(t, out.Categories[0], "Work", "the first parsed category")
}

// TestParseVTODOHostileInputs covers malformed iCalendar, non-VTODO objects, and
// invalid date values (RFC 5545 section 3.6.2).
func TestParseVTODOHostileInputs(t *testing.T) {
	for _, raw := range []string{
		"TRUNCATED",
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	} {
		task, uid, ok := ParseVTODO([]byte(raw))
		if ok || uid != "" || task.Subject != "" || !task.Start.IsZero() || !task.Due.IsZero() {
			t.Errorf("ParseVTODO(%q) = (%+v, %q, %v), want zero and false", raw, task, uid, ok)
		}
	}

	badDates := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t-bad\r\n" +
		"SUMMARY:Check\r\nDTSTART:not-a-date\r\nDUE:20260230T999999Z\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	task, uid, ok := ParseVTODO([]byte(badDates))
	if !ok || uid != "t-bad" {
		t.Fatalf("ParseVTODO valid VTODO with bad dates = (%q, %v), want (t-bad, true)", uid, ok)
	}
	wantTrue(t, task.Start.IsZero(), "invalid DTSTART ignored")
	wantTrue(t, task.Due.IsZero(), "invalid DUE ignored")
}
