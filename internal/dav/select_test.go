package dav

import (
	"net/http"
	"strings"
	"testing"
)

// TestSelectCalendarDataReport confirms a calendar-multiget with a CALDAV:comp/prop
// selection returns only the requested properties (RFC 4791 §9.6.1/§9.6.4).
func TestSelectCalendarDataReport(t *testing.T) {
	ts := davServerCal(t)
	base := "/dav/calendars/" + testUser + "/calendar/"
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:sel1\r\nSUMMARY:Meeting\r\n" +
		"DESCRIPTION:secret notes\r\nDTSTART:20260701T140000Z\r\nLOCATION:HQ\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if r := putCal(t, ts, "sel1.ics", ev, ""); r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201", r.StatusCode)
	}

	report := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-data><C:comp name="VCALENDAR"><C:prop name="VERSION"/>` +
		`<C:comp name="VEVENT"><C:prop name="UID"/><C:prop name="SUMMARY"/></C:comp></C:comp></C:calendar-data></D:prop>` +
		`<D:href>` + base + `sel1.ics</D:href></C:calendar-multiget>`
	resp, body := doFull(t, ts, "REPORT", base, report, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT status %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "UID:sel1") || !strings.Contains(body, "SUMMARY:Meeting") {
		t.Errorf("selected properties missing from the projection\n%s", body)
	}
	for _, leak := range []string{"DESCRIPTION", "LOCATION", "DTSTART"} {
		if strings.Contains(body, leak) {
			t.Errorf("unselected property %q leaked into the projection\n%s", leak, body)
		}
	}
}

// TestSelectAddressDataReport confirms an addressbook-multiget with a CARDDAV:address-
// data prop selection returns only the requested vCard properties (RFC 6352 §10.4).
func TestSelectAddressDataReport(t *testing.T) {
	ts := davServer(t)
	url := contactURL("sel.vcf")
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Grace Hopper\r\nEMAIL:grace@navy.test\r\n" +
		"TEL:+1-555-0000\r\nUID:g-1\r\nEND:VCARD\r\n"
	if r, b := doFull(t, ts, "PUT", url, card, map[string]string{"Content-Type": "text/vcard"}); r.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201\n%s", r.StatusCode, b)
	}

	report := `<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">` +
		`<D:prop><C:address-data><C:prop name="VERSION"/><C:prop name="FN"/><C:prop name="EMAIL"/></C:address-data></D:prop>` +
		`<D:href>` + url + `</D:href></C:addressbook-multiget>`
	resp, body := doFull(t, ts, "REPORT", contactURL(""), report, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT status %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "FN:Grace Hopper") || !strings.Contains(body, "grace@navy.test") {
		t.Errorf("selected properties missing from the projection\n%s", body)
	}
	if strings.Contains(body, "TEL:") {
		t.Errorf("unselected property TEL leaked into the projection\n%s", body)
	}
}
