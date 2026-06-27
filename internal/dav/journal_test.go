package dav

import (
	"net/http"
	"strings"
	"testing"
)

// TestJournalPutGetRoundTrip confirms a VJOURNAL PUT to the Journal collection round-
// trips its SUMMARY/DESCRIPTION/DTSTART on GET (RFC 5545 §3.6.3, task #116 verify).
func TestJournalPutGetRoundTrip(t *testing.T) {
	ts := davServerCal(t)
	url := "/dav/calendars/" + testUser + "/journal/j1.ics"
	vj := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VJOURNAL\r\nUID:j1\r\nSUMMARY:Lab notes\r\n" +
		"DESCRIPTION:Ran the experiment\r\nDTSTART:20260701T090000Z\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n"
	resp, body := doFull(t, ts, "PUT", url, vj, map[string]string{"Content-Type": "text/calendar"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201\n%s", resp.StatusCode, body)
	}

	resp, body = doFull(t, ts, "GET", url, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"BEGIN:VJOURNAL", "SUMMARY:Lab notes", "DESCRIPTION:Ran the experiment", "DTSTART:20260701T090000Z"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET round-trip lost %q\n%s", want, body)
		}
	}
}

// TestJournalCollectionAdvertised confirms the calendar home-set lists the Journal
// collection with a VJOURNAL supported-calendar-component-set.
func TestJournalCollectionAdvertised(t *testing.T) {
	ts := davServerCal(t)
	_, body := doFull(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/", "", map[string]string{"Depth": "1"})
	if !strings.Contains(body, "/journal/") {
		t.Errorf("calendar home-set does not advertise the journal collection\n%s", body)
	}
	if !strings.Contains(body, "VJOURNAL") {
		t.Errorf("home-set lacks the VJOURNAL supported-calendar-component-set\n%s", body)
	}
}
