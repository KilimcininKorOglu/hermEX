package dav

import (
	"net/http"
	"strings"
	"testing"
)

// TestLimitRecurrenceSetReport confirms a calendar-multiget carrying
// CALDAV:limit-recurrence-set returns the master (with its RRULE) plus only the
// overridden instance whose instant falls in the window, dropping the out-of-window
// override (RFC 4791 §9.6.6). The series is weekly; overrides sit on Jul 8 and Jul 22;
// the window [Jul 7, Jul 14) admits only Jul 8.
func TestLimitRecurrenceSetReport(t *testing.T) {
	ts := davServerCal(t)
	base := "/dav/calendars/" + testUser + "/calendar/"
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:lr\r\nDTSTART:20260701T140000Z\r\nDTEND:20260701T143000Z\r\nRRULE:FREQ=WEEKLY\r\nSUMMARY:Standup\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:lr\r\nRECURRENCE-ID:20260708T140000Z\r\nDTSTART:20260708T150000Z\r\nDTEND:20260708T153000Z\r\nSUMMARY:moved-near\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:lr\r\nRECURRENCE-ID:20260722T140000Z\r\nDTSTART:20260722T160000Z\r\nDTEND:20260722T163000Z\r\nSUMMARY:moved-far\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if r := putCal(t, ts, "lr.ics", ev, ""); r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201", r.StatusCode)
	}

	report := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-data><C:limit-recurrence-set start="20260707T000000Z" end="20260714T000000Z"/></C:calendar-data></D:prop>` +
		`<D:href>` + base + `lr.ics</D:href></C:calendar-multiget>`
	resp, body := doFull(t, ts, "REPORT", base, report, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT status %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "RRULE") {
		t.Errorf("limit-recurrence-set must keep the master RRULE\n%s", body)
	}
	if n := strings.Count(body, "RECURRENCE-ID"); n != 1 {
		t.Errorf("kept override count = %d, want 1 (only Jul 8)\n%s", n, body)
	}
	if strings.Contains(body, "moved-far") {
		t.Errorf("the out-of-window override must be dropped\n%s", body)
	}
	if !strings.Contains(body, "moved-near") {
		t.Errorf("the in-window override must be kept\n%s", body)
	}
}
