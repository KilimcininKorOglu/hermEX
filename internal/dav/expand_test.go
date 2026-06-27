package dav

import (
	"net/http"
	"strings"
	"testing"
)

// TestExpandRecurrenceReport confirms a calendar-multiget carrying CALDAV:expand
// returns a recurring event as one VEVENT per in-window instance, each with a
// RECURRENCE-ID and no RRULE (RFC 4791 §9.6.5). The series is weekly from a Wednesday
// with COUNT=5 (Jul 1/8/15/22/29 2026); the window [Jul 1, Jul 29) holds four.
func TestExpandRecurrenceReport(t *testing.T) {
	ts := davServerCal(t)
	base := "/dav/calendars/" + testUser + "/calendar/"
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:wk-1\r\nSUMMARY:Standup\r\n" +
		"DTSTART:20260701T140000Z\r\nDTEND:20260701T143000Z\r\nRRULE:FREQ=WEEKLY;COUNT=5\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if r := putCal(t, ts, "wk-1.ics", ev, ""); r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201", r.StatusCode)
	}

	report := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-data><C:expand start="20260701T000000Z" end="20260729T000000Z"/></C:calendar-data></D:prop>` +
		`<D:href>` + base + `wk-1.ics</D:href></C:calendar-multiget>`
	resp, body := doFull(t, ts, "REPORT", base, report, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT status %d, want 207\n%s", resp.StatusCode, body)
	}
	if n := strings.Count(body, "BEGIN:VEVENT"); n != 4 {
		t.Errorf("expanded VEVENT count = %d, want 4 (Jul 1/8/15/22)\n%s", n, body)
	}
	if n := strings.Count(body, "RECURRENCE-ID"); n != 4 {
		t.Errorf("RECURRENCE-ID count = %d, want 4\n%s", n, body)
	}
	if strings.Contains(body, "RRULE") {
		t.Errorf("expanded data must not carry RRULE\n%s", body)
	}
}

// TestExpandNonRecurringUnchanged confirms expand leaves a non-recurring object as-is
// (no RECURRENCE-ID synthesized for an event with no RRULE).
func TestExpandNonRecurringUnchanged(t *testing.T) {
	ts := davServerCal(t)
	base := "/dav/calendars/" + testUser + "/calendar/"
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:solo-1\r\nSUMMARY:Once\r\n" +
		"DTSTART:20260701T140000Z\r\nDTEND:20260701T143000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if r := putCal(t, ts, "solo-1.ics", ev, ""); r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201", r.StatusCode)
	}
	report := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-data><C:expand start="20260701T000000Z" end="20260729T000000Z"/></C:calendar-data></D:prop>` +
		`<D:href>` + base + `solo-1.ics</D:href></C:calendar-multiget>`
	resp, body := doFull(t, ts, "REPORT", base, report, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT status %d, want 207\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "RECURRENCE-ID") {
		t.Errorf("non-recurring object must not gain a RECURRENCE-ID\n%s", body)
	}
	if n := strings.Count(body, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("non-recurring VEVENT count = %d, want 1\n%s", n, body)
	}
}
