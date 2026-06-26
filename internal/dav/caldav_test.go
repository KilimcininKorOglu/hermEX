package dav

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// seedCalendar creates a mailbox seeded with the named appointments in the
// Calendar folder, each carrying its DAV resource name so the collection lists
// them as {name}.ics, and returns a StaticAccounts authorizing the test user.
func seedCalendar(t *testing.T, names ...string) directory.StaticAccounts {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{davResourceName})
	if err != nil {
		t.Fatal(err)
	}
	rnTag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode))
	for _, name := range names {
		msg := &oxcmail.Message{Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
			{Tag: mapi.PrSubject, Value: name},
			{Tag: rnTag, Value: name + ".ics"},
		}}
		if _, err := st.CreateMessage(mapi.PrivateFIDCalendar, msg); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	return directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
}

// davServerCal starts a test DAV server over a mailbox seeded with the given
// appointments.
func davServerCal(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	accs := seedCalendar(t, names...)
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestOptionsCalendarAccess confirms OPTIONS advertises CalDAV support.
func TestOptionsCalendarAccess(t *testing.T) {
	ts := davServerCal(t)
	resp, _ := do(t, ts, "OPTIONS", "/dav/calendars/"+testUser+"/calendar/", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if dav := resp.Header.Get("DAV"); !strings.Contains(dav, "calendar-access") {
		t.Errorf("DAV header %q lacks calendar-access", dav)
	}
}

// TestPrincipalCalendarHomeSet checks the discovery chain advertises the
// calendar-home-set alongside the addressbook one.
func TestPrincipalCalendarHomeSet(t *testing.T) {
	ts := davServerCal(t)
	resp, body := do(t, ts, "PROPFIND", "/dav/principals/"+testUser+"/", "0", true)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "calendar-home-set") || !strings.Contains(body, "/dav/calendars/"+testUser+"/") {
		t.Errorf("principal response lacks calendar-home-set\n%s", body)
	}
}

// TestPropfindCalendar checks a Depth 1 PROPFIND on the Calendar collection
// returns 207 with the calendar resourcetype, its CTag, the supported component
// set, and one entry per seeded appointment.
func TestPropfindCalendar(t *testing.T) {
	ts := davServerCal(t, "Standup", "Review")
	resp, body := do(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/calendar/", "1", true)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"multistatus", "calendar", "getctag", "VEVENT", ".ics", "getetag"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	if n := strings.Count(body, ".ics"); n != 2 {
		t.Errorf("got %d .ics hrefs, want 2\n%s", n, body)
	}
}

func calURL(name string) string {
	return "/dav/calendars/" + testUser + "/calendar/" + name
}

const timedEventICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
	"UID:cal-1\r\nSUMMARY:Planning\r\nDTSTART:20260615T140000Z\r\nDTEND:20260615T150000Z\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

const recurringEventICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:rec-1\r\nSUMMARY:Weekly\r\n" +
	"DTSTART:20260615T140000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// TestCalPutGetRoundTrip stores a VEVENT with PUT and reads it back, confirming
// the event survives conversion to MAPI and back and is listed in the collection.
func TestCalPutGetRoundTrip(t *testing.T) {
	ts := davServerCal(t)
	url := calURL("plan.ics")

	resp, body := doFull(t, ts, "PUT", url, timedEventICS, map[string]string{"Content-Type": "text/calendar"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("PUT did not return an ETag")
	}

	resp, body = doFull(t, ts, "GET", url, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("GET content-type %q", ct)
	}
	for _, want := range []string{"BEGIN:VCALENDAR", "SUMMARY:Planning", "DTSTART:20260615T140000Z", "END:VCALENDAR"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET body missing %q\n%s", want, body)
		}
	}
	_, pf := doFull(t, ts, "PROPFIND", calURL(""), "", map[string]string{"Depth": "1"})
	if !strings.Contains(pf, "plan.ics") {
		t.Errorf("PROPFIND lacks the PUT resource name plan.ics\n%s", pf)
	}
}

// TestCalRecurringVerbatim confirms a recurring event survives PUT/GET with its
// RRULE intact (stored verbatim, not synthesized).
func TestCalRecurringVerbatim(t *testing.T) {
	ts := davServerCal(t)
	url := calURL("weekly.ics")
	resp, body := doFull(t, ts, "PUT", url, recurringEventICS, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201\n%s", resp.StatusCode, body)
	}
	_, get := doFull(t, ts, "GET", url, "", nil)
	if !strings.Contains(get, "RRULE:FREQ=WEEKLY;BYDAY=MO") {
		t.Errorf("recurring GET lost its RRULE\n%s", get)
	}
}

// TestCalIfMatchConflict confirms a stale If-Match is rejected with 412.
func TestCalIfMatchConflict(t *testing.T) {
	ts := davServerCal(t)
	url := calURL("plan.ics")
	doFull(t, ts, "PUT", url, timedEventICS, nil)
	resp, _ := doFull(t, ts, "PUT", url, timedEventICS, map[string]string{"If-Match": `"99999"`})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status %d, want 412", resp.StatusCode)
	}
}

// TestCalDelete removes an event and confirms it is then absent.
func TestCalDelete(t *testing.T) {
	ts := davServerCal(t)
	url := calURL("plan.ics")
	doFull(t, ts, "PUT", url, timedEventICS, nil)

	resp, _ := doFull(t, ts, "DELETE", url, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d, want 204", resp.StatusCode)
	}
	resp, _ = doFull(t, ts, "GET", url, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete: status %d, want 404", resp.StatusCode)
	}
}

// TestCalReportMultiget requests a named event and confirms its calendar-data
// comes back.
func TestCalReportMultiget(t *testing.T) {
	ts := davServerCal(t)
	doFull(t, ts, "PUT", calURL("plan.ics"), timedEventICS, nil)

	body := `<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <d:href>` + calURL("plan.ics") + `</d:href>
</c:calendar-multiget>`
	resp, out := doFull(t, ts, "REPORT", calURL(""), body, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{"SUMMARY:Planning", "calendar-data", "getetag"} {
		if !strings.Contains(out, want) {
			t.Errorf("multiget missing %q\n%s", want, out)
		}
	}
}

// TestCalReportQuery confirms calendar-query returns every member's
// calendar-data unfiltered and without a sync-token (it is not an incremental
// sync). This is the report Apple Calendar and iOS fire first on a freshly
// added account, so a regression here would break initial population even when
// multiget and sync-collection still pass.
func TestCalReportQuery(t *testing.T) {
	ts := davServerCal(t)
	doFull(t, ts, "PUT", calURL("plan.ics"), timedEventICS, nil)
	review := strings.Replace(timedEventICS, "UID:cal-1\r\nSUMMARY:Planning", "UID:cal-2\r\nSUMMARY:Review", 1)
	doFull(t, ts, "PUT", calURL("review.ics"), review, nil)

	body := `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"/></c:comp-filter></c:filter>
</c:calendar-query>`
	resp, out := doFull(t, ts, "REPORT", calURL(""), body, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{"SUMMARY:Planning", "SUMMARY:Review", "calendar-data"} {
		if !strings.Contains(out, want) {
			t.Errorf("calendar-query missing %q\n%s", want, out)
		}
	}
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("calendar-query returned %d events, want 2\n%s", n, out)
	}
	if token := syncTokenRE.FindString(out); token != "" {
		t.Errorf("calendar-query must not carry a sync-token, got %q", token)
	}
}

// TestCalReportQueryFilter confirms calendar-query applies the <filter>: a
// time-range selects only the overlapping event, and a UID prop-filter text-match
// selects only the matching one (RFC 4791 §9.7).
func TestCalReportQueryFilter(t *testing.T) {
	ts := davServerCal(t)
	doFull(t, ts, "PUT", calURL("june.ics"), timedEventICS, nil) // UID:cal-1, DTSTART June 15
	dec := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
		"UID:cal-9\r\nSUMMARY:Yearend\r\nDTSTART:20261215T140000Z\r\nDTEND:20261215T150000Z\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	doFull(t, ts, "PUT", calURL("dec.ics"), dec, nil)

	timeRange := `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
    <c:time-range start="20260601T000000Z" end="20260701T000000Z"/>
  </c:comp-filter></c:comp-filter></c:filter>
</c:calendar-query>`
	resp, out := doFull(t, ts, "REPORT", calURL(""), timeRange, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "SUMMARY:Planning") || strings.Contains(out, "SUMMARY:Yearend") {
		t.Errorf("time-range filter returned the wrong set (want June only)\n%s", out)
	}
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("time-range filter returned %d events, want 1\n%s", n, out)
	}

	uidFilter := `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><c:calendar-data/></d:prop>
  <c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
    <c:prop-filter name="UID"><c:text-match>cal-9</c:text-match></c:prop-filter>
  </c:comp-filter></c:comp-filter></c:filter>
</c:calendar-query>`
	_, out = doFull(t, ts, "REPORT", calURL(""), uidFilter, map[string]string{"Depth": "1"})
	if !strings.Contains(out, "SUMMARY:Yearend") || strings.Contains(out, "SUMMARY:Planning") {
		t.Errorf("UID prop-filter returned the wrong set (want cal-9 only)\n%s", out)
	}
}

// TestCalReportSync checks incremental sync: an initial sync returns the member
// and a token; after a new PUT, a sync with that token returns only the new one.
func TestCalReportSync(t *testing.T) {
	ts := davServerCal(t)
	doFull(t, ts, "PUT", calURL("plan.ics"), timedEventICS, nil)

	initial := `<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	resp, out := doFull(t, ts, "REPORT", calURL(""), initial, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("initial sync status %d, want 207\n%s", resp.StatusCode, out)
	}
	token := syncTokenRE.FindString(out)
	if token == "" {
		t.Fatalf("initial sync returned no sync-token\n%s", out)
	}

	second := strings.Replace(recurringEventICS, "rec-1", "rec-2", 1)
	doFull(t, ts, "PUT", calURL("weekly.ics"), second, nil)

	next := `<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + token + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	resp, out = doFull(t, ts, "REPORT", calURL(""), next, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("incremental sync status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "weekly.ics") {
		t.Errorf("incremental sync missing the new member\n%s", out)
	}
	if strings.Contains(out, "plan.ics") {
		t.Errorf("incremental sync re-sent an unchanged member\n%s", out)
	}
}

// TestCalReportSyncDelete checks deletion reporting: after a member is deleted, a
// sync with the prior token returns it as a 404 tombstone, and the advanced token
// does not re-report it (RFC 6578).
func TestCalReportSyncDelete(t *testing.T) {
	ts := davServerCal(t)
	doFull(t, ts, "PUT", calURL("plan.ics"), timedEventICS, nil)

	initial := `<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	_, out := doFull(t, ts, "REPORT", calURL(""), initial, nil)
	token := syncTokenRE.FindString(out)
	if token == "" {
		t.Fatalf("initial sync returned no sync-token\n%s", out)
	}

	if resp, _ := doFull(t, ts, "DELETE", calURL("plan.ics"), "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d, want 204", resp.StatusCode)
	}

	follow := `<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + token + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	resp, out := doFull(t, ts, "REPORT", calURL(""), follow, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("sync status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "plan.ics") || !strings.Contains(out, "404") {
		t.Errorf("sync after delete missing the 404 tombstone for plan.ics\n%s", out)
	}
	token2 := syncTokenRE.FindString(out)

	again := `<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + token2 + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	_, out = doFull(t, ts, "REPORT", calURL(""), again, nil)
	if strings.Contains(out, "plan.ics") {
		t.Errorf("tombstone re-reported on the next sync (token did not advance past the delete)\n%s", out)
	}
}

// TestCalFreeBusy confirms free-busy-query aggregates the in-range busy events into
// a VFREEBUSY component and excludes out-of-range ones (RFC 4791 §7.10).
func TestCalFreeBusy(t *testing.T) {
	ts := davServerCal(t)
	doFull(t, ts, "PUT", calURL("june.ics"), timedEventICS, nil) // June 15 14:00-15:00Z
	dec := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
		"UID:cal-9\r\nSUMMARY:Yearend\r\nDTSTART:20261215T140000Z\r\nDTEND:20261215T150000Z\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	doFull(t, ts, "PUT", calURL("dec.ics"), dec, nil)

	body := `<c:free-busy-query xmlns:c="urn:ietf:params:xml:ns:caldav">
  <c:time-range start="20260601T000000Z" end="20260701T000000Z"/>
</c:free-busy-query>`
	resp, out := doFull(t, ts, "REPORT", calURL(""), body, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free-busy status %d, want 200\n%s", resp.StatusCode, out)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("free-busy content-type %q, want text/calendar", ct)
	}
	for _, want := range []string{"BEGIN:VFREEBUSY", "FREEBUSY;FBTYPE=BUSY:20260615T140000Z/20260615T150000Z", "END:VFREEBUSY"} {
		if !strings.Contains(out, want) {
			t.Errorf("free-busy missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "20261215") {
		t.Errorf("free-busy included the out-of-range December event\n%s", out)
	}
}

// TestMkCalendarCollection confirms MKCALENDAR creates a usable second calendar,
// rejects re-creation, isolates its objects from the default calendar, and that the
// home set then lists both (RFC 4791 §5.3.1).
func TestMkCalendarCollection(t *testing.T) {
	ts := davServerCal(t)
	work := "/dav/calendars/" + testUser + "/work/"

	resp, _ := doFull(t, ts, "MKCALENDAR", work, "", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCALENDAR status %d, want 201", resp.StatusCode)
	}
	resp, _ = doFull(t, ts, "MKCALENDAR", work, "", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("MKCALENDAR on existing collection: status %d, want 405", resp.StatusCode)
	}

	resp, _ = doFull(t, ts, "PUT", work+"ev.ics", timedEventICS, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT into new calendar: status %d, want 201", resp.StatusCode)
	}
	resp, body := doFull(t, ts, "GET", work+"ev.ics", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Planning") {
		t.Errorf("GET from new calendar: status %d\n%s", resp.StatusCode, body)
	}
	// The object must not leak into the well-known calendar.
	if resp, _ := doFull(t, ts, "GET", calURL("ev.ics"), "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("event leaked into the default calendar: status %d, want 404", resp.StatusCode)
	}

	_, body = doFull(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/", "", map[string]string{"Depth": "1"})
	if !strings.Contains(body, "/calendar/") || !strings.Contains(body, "/work/") {
		t.Errorf("calendar home set did not list both collections\n%s", body)
	}
}

// TestCalProppatch confirms PROPPATCH stores a dead property that PROPFIND replays,
// rejects a protected property with 403, lets DAV:displayname override the default
// label, and removes a property on request (RFC 4918 §9.2).
func TestCalProppatch(t *testing.T) {
	ts := davServerCal(t)
	cal := calURL("")

	setColor := `<d:propertyupdate xmlns:d="DAV:" xmlns:x="http://apple.com/ns/ical/">` +
		`<d:set><d:prop><x:calendar-color>#FF2968</x:calendar-color></d:prop></d:set></d:propertyupdate>`
	resp, out := doFull(t, ts, "PROPPATCH", cal, setColor, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "calendar-color") || !strings.Contains(out, "200 OK") {
		t.Errorf("PROPPATCH did not report the property as set\n%s", out)
	}

	_, out = doFull(t, ts, "PROPFIND", cal, "", map[string]string{"Depth": "0"})
	if !strings.Contains(out, "#FF2968") {
		t.Errorf("PROPFIND did not round-trip the stored dead property\n%s", out)
	}

	setEtag := `<d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><d:getetag>x</d:getetag></d:prop></d:set></d:propertyupdate>`
	resp, out = doFull(t, ts, "PROPPATCH", cal, setEtag, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("protected PROPPATCH status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "403 Forbidden") {
		t.Errorf("protected property was not rejected with 403\n%s", out)
	}

	setName := `<d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><d:displayname>Work Cal</d:displayname></d:prop></d:set></d:propertyupdate>`
	if resp, _ := doFull(t, ts, "PROPPATCH", cal, setName, nil); resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("displayname PROPPATCH status %d, want 207", resp.StatusCode)
	}
	_, out = doFull(t, ts, "PROPFIND", cal, "", map[string]string{"Depth": "0"})
	if !strings.Contains(out, "Work Cal") || strings.Contains(out, ">Calendar<") {
		t.Errorf("PROPFIND did not replace the default displayname with the stored one\n%s", out)
	}

	rmColor := `<d:propertyupdate xmlns:d="DAV:" xmlns:x="http://apple.com/ns/ical/">` +
		`<d:remove><d:prop><x:calendar-color/></d:prop></d:remove></d:propertyupdate>`
	if resp, _ := doFull(t, ts, "PROPPATCH", cal, rmColor, nil); resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("remove PROPPATCH status %d, want 207", resp.StatusCode)
	}
	_, out = doFull(t, ts, "PROPFIND", cal, "", map[string]string{"Depth": "0"})
	if strings.Contains(out, "#FF2968") {
		t.Errorf("removed dead property is still present\n%s", out)
	}
}
