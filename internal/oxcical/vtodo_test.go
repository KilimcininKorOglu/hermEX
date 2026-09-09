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
