package dav

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// putCal issues a calendar PUT as the test user, optionally with an
// If-Schedule-Tag-Match precondition, and returns the response.
func putCal(t *testing.T, ts *httptest.Server, name, body, ifSchedule string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("PUT", ts.URL+"/dav/calendars/"+testUser+"/calendar/"+name, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	if ifSchedule != "" {
		req.Header.Set("If-Schedule-Tag-Match", ifSchedule)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// TestScheduleTagInReport confirms a calendar-multiget REPORT exposes the
// CALDAV:schedule-tag property for a scheduling object but not for a plain
// appointment (RFC 6638 3.2.10).
func TestScheduleTagInReport(t *testing.T) {
	ts := davServerCal(t)
	base := "/dav/calendars/" + testUser + "/calendar/"
	putCal(t, ts, "rm.ics", schedEvent("rm", testUser, "Meet", "20260701T140000Z", 0, "bob@hermex.test"), "")
	plain := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:rp\r\nSUMMARY:Solo\r\nDTSTART:20260701T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	putCal(t, ts, "rp.ics", plain, "")

	report := `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><D:getetag/><C:schedule-tag/><C:calendar-data/></D:prop>` +
		`<D:href>` + base + `rm.ics</D:href><D:href>` + base + `rp.ics</D:href></C:calendar-multiget>`
	resp, body := doFull(t, ts, "REPORT", base, report, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("REPORT status %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "rm.ics") || !strings.Contains(body, "rp.ics") {
		t.Fatalf("REPORT did not return both members\n%s", body)
	}
	if !strings.Contains(body, "schedule-tag") {
		t.Errorf("REPORT did not expose schedule-tag for the meeting\n%s", body)
	}
	if n := strings.Count(body, "<schedule-tag"); n != 1 {
		t.Errorf("expected exactly one schedule-tag element (the meeting), got %d\n%s", n, body)
	}
}

// TestScheduleTagHeaders covers RFC 6638 8.2/8.3: a scheduling object reports a
// Schedule-Tag (a plain appointment does not), the tag changes on every PUT (3.2.10
// rule 3), and If-Schedule-Tag-Match gates the PUT.
func TestScheduleTagHeaders(t *testing.T) {
	ts := davServerCal(t)
	meeting := schedEvent("st-1", testUser, "Sync", "20260701T140000Z", 0, "bob@hermex.test")

	r1 := putCal(t, ts, "st-1.ics", meeting, "")
	st1 := r1.Header.Get("Schedule-Tag")
	if st1 == "" {
		t.Fatal("PUT of a meeting returned no Schedule-Tag")
	}

	g, _ := do(t, ts, "GET", "/dav/calendars/"+testUser+"/calendar/st-1.ics", "", true)
	if g.Header.Get("Schedule-Tag") == "" || g.Header.Get("ETag") == "" {
		t.Errorf("GET of a meeting lacks Schedule-Tag (%q) or ETag (%q)", g.Header.Get("Schedule-Tag"), g.Header.Get("ETag"))
	}

	// A plain appointment (no ORGANIZER) is not a scheduling object: no Schedule-Tag.
	plain := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:p-1\r\nSUMMARY:Solo\r\nDTSTART:20260701T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if rp := putCal(t, ts, "p-1.ics", plain, ""); rp.Header.Get("Schedule-Tag") != "" {
		t.Errorf("plain appointment got a Schedule-Tag %q", rp.Header.Get("Schedule-Tag"))
	}

	// Re-PUT bumps the schedule-tag (rule 3).
	r2 := putCal(t, ts, "st-1.ics", schedEvent("st-1", testUser, "Sync v2", "20260701T150000Z", 1, "bob@hermex.test"), "")
	st2 := r2.Header.Get("Schedule-Tag")
	if st2 == "" || st2 == st1 {
		t.Errorf("re-PUT did not change the schedule-tag: %q then %q", st1, st2)
	}

	// If-Schedule-Tag-Match: a stale tag fails 412, the current tag succeeds.
	if rstale := putCal(t, ts, "st-1.ics", meeting, st1); rstale.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("stale If-Schedule-Tag-Match: status %d, want 412", rstale.StatusCode)
	}
	if rok := putCal(t, ts, "st-1.ics", meeting, st2); rok.StatusCode != http.StatusNoContent {
		t.Errorf("matching If-Schedule-Tag-Match: status %d, want 204", rok.StatusCode)
	}
}

// TestScheduleReplySuppress confirms an attendee's DELETE with Schedule-Reply:F sends
// no reply to the organizer (RFC 6638 8.1).
func TestScheduleReplySuppress(t *testing.T) {
	aliceDir := filepath.Join(t.TempDir(), "alice")
	bobDir := filepath.Join(t.TempDir(), "bob")
	for _, d := range []string{aliceDir, bobDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accs := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: aliceDir},
		"bob@hermex.test": {Password: testPass, MailboxPath: bobDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)

	// bob (the attendee) files the invite into his calendar with PARTSTAT=ACCEPTED,
	// which sends his reply to alice.
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:r-1\r\nDTSTART:20260701T140000Z\r\nSEQUENCE:0\r\n" +
		"ORGANIZER:mailto:" + testUser + "\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if resp, out := bobDav(t, ts, "PUT", "/dav/calendars/bob@hermex.test/calendar/r-1.ics", "", ev); resp.StatusCode != http.StatusCreated {
		t.Fatalf("attendee accept PUT status %d, want 201\n%s", resp.StatusCode, out)
	}
	if n, _, _ := inboxMessage(t, aliceDir); n != 1 {
		t.Fatalf("after accept, alice inbox has %d messages, want 1 (the reply)", n)
	}

	// bob deletes with Schedule-Reply:F: no DECLINED reply is sent.
	req, err := http.NewRequest("DELETE", ts.URL+"/dav/calendars/bob@hermex.test/calendar/r-1.ics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("bob@hermex.test", testPass)
	req.Header.Set("Schedule-Reply", "F")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("attendee DELETE status %d, want 204", resp.StatusCode)
	}
	if n, _, _ := inboxMessage(t, aliceDir); n != 1 {
		t.Errorf("Schedule-Reply:F still sent a reply: alice inbox has %d, want still 1", n)
	}
}
