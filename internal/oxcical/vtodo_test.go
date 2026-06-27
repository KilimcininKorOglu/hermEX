package oxcical

import (
	"strings"
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
	if !strings.Contains(s, "BEGIN:VTODO") || !strings.Contains(s, "SUMMARY:Ship release") {
		t.Fatalf("VTODO missing core fields:\n%s", s)
	}
	if !strings.Contains(s, "STATUS:COMPLETED") || !strings.Contains(s, "DUE:20260701T170000Z") {
		t.Errorf("VTODO missing status/due:\n%s", s)
	}

	out, uid, ok := ParseVTODO(ics)
	if !ok {
		t.Fatal("ParseVTODO returned ok=false")
	}
	if uid != "task-1@hermex.test" {
		t.Errorf("uid = %q", uid)
	}
	if out.Subject != in.Subject || out.Body != in.Body {
		t.Errorf("subject/body = %q/%q", out.Subject, out.Body)
	}
	if !out.Due.Equal(in.Due) || !out.Start.Equal(in.Start) {
		t.Errorf("start/due = %v / %v", out.Start, out.Due)
	}
	if !out.Complete || out.Importance != 2 {
		t.Errorf("complete=%v importance=%d", out.Complete, out.Importance)
	}
	if len(out.Categories) != 2 || out.Categories[0] != "Work" {
		t.Errorf("categories = %v", out.Categories)
	}
}
