package oxcical

import (
	"strings"
	"testing"
	"time"
)

const recurWithOverrides = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:wk\r\nDTSTART:20260701T140000Z\r\nDTEND:20260701T143000Z\r\n" +
	"RRULE:FREQ=WEEKLY\r\nSUMMARY:Standup\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:wk\r\nRECURRENCE-ID:20260708T140000Z\r\nDTSTART:20260708T150000Z\r\n" +
	"DTEND:20260708T153000Z\r\nSUMMARY:moved-near\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:wk\r\nRECURRENCE-ID:20260722T140000Z\r\nDTSTART:20260722T160000Z\r\n" +
	"DTEND:20260722T163000Z\r\nSUMMARY:moved-far\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestLimitRecurrenceSet keeps the master (with its RRULE) plus only the override whose
// instant falls in the window, dropping the out-of-window override (RFC 4791 §9.6.6).
func TestLimitRecurrenceSet(t *testing.T) {
	out, ok := LimitRecurrenceSet([]byte(recurWithOverrides),
		time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("LimitRecurrenceSet returned ok=false for a recurring object with overrides")
	}
	s := string(out)
	if !strings.Contains(s, "RRULE:FREQ=WEEKLY") {
		t.Errorf("master with RRULE must be kept\n%s", s)
	}
	if n := strings.Count(s, "RECURRENCE-ID"); n != 1 {
		t.Errorf("kept override count = %d, want 1 (only Jul 8)\n%s", n, s)
	}
	if !strings.Contains(s, "moved-near") || strings.Contains(s, "moved-far") {
		t.Errorf("expected the in-window override kept and the out-of-window one dropped\n%s", s)
	}
}

// TestLimitRecurrenceSetNoOverrides leaves a master-only recurring object untouched
// (ok=false → caller serves it unchanged).
func TestLimitRecurrenceSetNoOverrides(t *testing.T) {
	master := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:wk\r\nDTSTART:20260701T140000Z\r\n" +
		"RRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if _, ok := LimitRecurrenceSet([]byte(master),
		time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)); ok {
		t.Error("a master-only object should report ok=false (nothing to trim)")
	}
}

// TestLimitFreeBusySet keeps only the FREEBUSY periods intersecting the window.
func TestLimitFreeBusySet(t *testing.T) {
	fb := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VFREEBUSY\r\nDTSTART:20260701T000000Z\r\n" +
		"DTEND:20260731T000000Z\r\nFREEBUSY:20260705T120000Z/20260705T130000Z,20260720T120000Z/20260720T130000Z\r\n" +
		"END:VFREEBUSY\r\nEND:VCALENDAR\r\n"
	out, ok := LimitFreeBusySet([]byte(fb),
		time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("LimitFreeBusySet returned ok=false for an object with a VFREEBUSY")
	}
	s := string(out)
	if !strings.Contains(s, "20260720T120000Z") {
		t.Errorf("the in-window FREEBUSY period must be kept\n%s", s)
	}
	if strings.Contains(s, "20260705T120000Z") {
		t.Errorf("the out-of-window FREEBUSY period must be dropped\n%s", s)
	}
}

// TestLimitFreeBusySetDuration handles a start/duration period form.
func TestLimitFreeBusySetDuration(t *testing.T) {
	fb := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VFREEBUSY\r\n" +
		"FREEBUSY:20260720T120000Z/PT1H\r\nEND:VFREEBUSY\r\nEND:VCALENDAR\r\n"
	out, ok := LimitFreeBusySet([]byte(fb),
		time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC))
	if !ok || !strings.Contains(string(out), "20260720T120000Z/PT1H") {
		t.Errorf("a start/duration period intersecting the window must be kept\n%s", string(out))
	}
}
