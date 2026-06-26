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
	"hermex/internal/oxcical"
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

// TestCalCopyMove confirms COPY duplicates an event into another calendar (keeping
// the source), honours Overwrite:F and the same-resource rule, and MOVE relocates
// the event and removes the source (RFC 4918 §9.8/§9.9).
func TestCalCopyMove(t *testing.T) {
	ts := davServerCal(t)
	work := "/dav/calendars/" + testUser + "/work/"
	doFull(t, ts, "MKCALENDAR", work, "", nil)
	doFull(t, ts, "PUT", calURL("ev.ics"), timedEventICS, nil)

	resp, _ := doFull(t, ts, "COPY", calURL("ev.ics"), "", map[string]string{"Destination": work + "copy.ics"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("COPY status %d, want 201", resp.StatusCode)
	}
	if resp, _ := doFull(t, ts, "GET", calURL("ev.ics"), "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("COPY removed the source: status %d, want 200", resp.StatusCode)
	}
	if resp, body := doFull(t, ts, "GET", work+"copy.ics", "", nil); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Planning") {
		t.Errorf("COPY destination missing or wrong: status %d\n%s", resp.StatusCode, body)
	}

	if resp, _ := doFull(t, ts, "COPY", calURL("ev.ics"), "", map[string]string{"Destination": work + "copy.ics", "Overwrite": "F"}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("COPY Overwrite:F onto existing: status %d, want 412", resp.StatusCode)
	}
	if resp, _ := doFull(t, ts, "COPY", calURL("ev.ics"), "", map[string]string{"Destination": calURL("ev.ics")}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("COPY to the same resource: status %d, want 403", resp.StatusCode)
	}

	if resp, _ := doFull(t, ts, "MOVE", calURL("ev.ics"), "", map[string]string{"Destination": work + "moved.ics"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("MOVE status %d, want 201", resp.StatusCode)
	}
	if resp, _ := doFull(t, ts, "GET", calURL("ev.ics"), "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("MOVE did not remove the source: status %d, want 404", resp.StatusCode)
	}
	if resp, _ := doFull(t, ts, "GET", work+"moved.ics", "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("MOVE destination missing: status %d, want 200", resp.StatusCode)
	}
}

// seedTimedCalendar imports a timed iCalendar event into a mailbox's Calendar
// through the same converter a PUT uses, so the busy span round-trips.
func seedTimedCalendar(t *testing.T, dir, ics string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msg, err := oxcical.Import([]byte(ics), icalOptions(st))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMessage(mapi.PrivateFIDCalendar, msg); err != nil {
		t.Fatal(err)
	}
}

// freeBusyRequest is an iTIP free-busy request body for an Outbox POST.
func freeBusyRequest(organizer, attendee string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VFREEBUSY\r\nUID:fb-1\r\nDTSTART:20260601T000000Z\r\nDTEND:20260701T000000Z\r\n" +
		"ORGANIZER:mailto:" + organizer + "\r\nATTENDEE:mailto:" + attendee + "\r\n" +
		"END:VFREEBUSY\r\nEND:VCALENDAR\r\n"
}

// TestOutboxFreeBusy confirms a scheduling Outbox POST returns each local
// attendee's busy periods in a schedule-response (RFC 6638 §5).
func TestOutboxFreeBusy(t *testing.T) {
	const bob = "bob@hermex.test"
	bobDir := filepath.Join(t.TempDir(), "bob")
	seedTimedCalendar(t, bobDir, timedEventICS) // bob busy June 15 14:00-15:00Z
	accs := directory.StaticAccounts{
		testUser: {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "alice")},
		bob:      {Password: testPass, MailboxPath: bobDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)

	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", freeBusyRequest(testUser, bob), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Outbox POST status %d, want 200\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{
		"schedule-response", bob, "2.0;Success",
		"BEGIN:VFREEBUSY", "FREEBUSY;FBTYPE=BUSY:20260615T140000Z/20260615T150000Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule-response missing %q\n%s", want, out)
		}
	}
}

// TestOutboxValidOrganizer confirms the Outbox rejects a POST whose ORGANIZER is
// not a calendar address of the Outbox owner (RFC 6638 §5.2.6, valid-organizer).
func TestOutboxValidOrganizer(t *testing.T) {
	ts := davServerCal(t)
	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", freeBusyRequest("eve@evil.test", testUser), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Outbox POST with foreign ORGANIZER: status %d, want 403\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "valid-organizer") {
		t.Errorf("403 body lacks the valid-organizer precondition\n%s", out)
	}
}

// TestOutboxRemoteRecipient confirms a non-local attendee is reported as an invalid
// calendar user rather than queried remotely (no iSchedule).
func TestOutboxRemoteRecipient(t *testing.T) {
	ts := davServerCal(t)
	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", freeBusyRequest(testUser, "remote@elsewhere.example"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Outbox POST status %d, want 200\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "3.7;Invalid calendar user") {
		t.Errorf("schedule-response did not mark the remote recipient invalid\n%s", out)
	}
}

// davServerWithPeer starts a DAV server whose directory knows the test user (the
// Outbox owner) plus a local peer "bob"; it returns the server and bob's mailbox
// directory so a test can inspect what was delivered to him. No relay spool is wired,
// so delivery is local-only.
func davServerWithPeer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	bobDir := filepath.Join(t.TempDir(), "bob")
	st, err := objectstore.Open(bobDir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "alice")},
		"bob@hermex.test": {Password: testPass, MailboxPath: bobDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, bobDir
}

// inboxMessage reports the count, message class, and subject of the first message in
// a mailbox's Inbox, so a delivery test can confirm what landed there.
func inboxMessage(t *testing.T, dir string) (count int, class, subject string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) == 0 {
		return 0, "", ""
	}
	msg, err := st.OpenMessage(objs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range msg.Props {
		switch p.Tag {
		case mapi.PrMessageClass:
			class, _ = p.Value.(string)
		case mapi.PrSubject:
			subject, _ = p.Value.(string)
		}
	}
	return len(objs), class, subject
}

func meetingRequestICS(organizer, attendee string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:mtg-1\r\nSUMMARY:Planning\r\nDTSTART:20260615T140000Z\r\nDTEND:20260615T150000Z\r\n" +
		"ORGANIZER:mailto:" + organizer + "\r\nATTENDEE:mailto:" + attendee + "\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// TestOutboxDeliverRequest confirms an organizer's Outbox POST of a METHOD:REQUEST
// is delivered to a local attendee's mailbox as a meeting request and reported as
// successfully scheduled (RFC 6638 §5.2).
func TestOutboxDeliverRequest(t *testing.T) {
	ts, bobDir := davServerWithPeer(t)
	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", meetingRequestICS(testUser, "bob@hermex.test"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Outbox POST status %d, want 200\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "bob@hermex.test") || !strings.Contains(out, "2.0;Success") {
		t.Errorf("schedule-response did not report bob scheduled\n%s", out)
	}
	n, class, subject := inboxMessage(t, bobDir)
	if n != 1 {
		t.Fatalf("bob's inbox has %d messages, want 1", n)
	}
	if !strings.Contains(class, "Schedule.Meeting.Request") {
		t.Errorf("delivered message class %q, want a meeting request", class)
	}
	if subject != "Planning" {
		t.Errorf("delivered subject %q, want Planning", subject)
	}
}

// TestOutboxDeliverValidOrganizer confirms a request whose ORGANIZER is not the
// Outbox owner is rejected (RFC 6638 §5.2.6, valid-organizer).
func TestOutboxDeliverValidOrganizer(t *testing.T) {
	ts := davServerCal(t)
	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", meetingRequestICS("eve@evil.test", testUser), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "valid-organizer") {
		t.Errorf("403 body lacks the valid-organizer precondition\n%s", out)
	}
}

// TestOutboxDeliverRemote confirms a non-local attendee is reported undeliverable
// when no relay spool is wired (delivery is local-only).
func TestOutboxDeliverRemote(t *testing.T) {
	ts := davServerCal(t)
	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", meetingRequestICS(testUser, "remote@elsewhere.example"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "3.7;Invalid calendar user") {
		t.Errorf("schedule-response did not mark the remote recipient undeliverable\n%s", out)
	}
}

// TestOutboxDeliverReply confirms an attendee's Outbox POST of a METHOD:REPLY is
// delivered to the organizer as a meeting response.
func TestOutboxDeliverReply(t *testing.T) {
	ts, bobDir := davServerWithPeer(t)
	reply := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REPLY\r\n" +
		"BEGIN:VEVENT\r\nUID:mtg-1\r\nSUMMARY:Planning\r\nDTSTART:20260615T140000Z\r\nDTEND:20260615T150000Z\r\n" +
		"ORGANIZER:mailto:bob@hermex.test\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:" + testUser + "\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	resp, out := doFull(t, ts, "POST", "/dav/calendars/"+testUser+"/outbox/", reply, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "bob@hermex.test") || !strings.Contains(out, "2.0;Success") {
		t.Errorf("schedule-response did not report the reply delivered\n%s", out)
	}
	n, class, _ := inboxMessage(t, bobDir)
	if n != 1 {
		t.Fatalf("organizer's inbox has %d messages, want 1", n)
	}
	if !strings.Contains(class, "Schedule.Meeting.Resp") {
		t.Errorf("delivered message class %q, want a meeting response", class)
	}
}

// TestScheduleDiscovery confirms the principal advertises the CalDAV scheduling
// discovery properties (RFC 6638 §2.2/§2.4.1): the calendar user address set and the
// scheduling Inbox/Outbox URLs a client needs to drive auto-scheduling.
func TestScheduleDiscovery(t *testing.T) {
	ts := davServerCal(t)
	resp, body := do(t, ts, "PROPFIND", "/dav/principals/"+testUser+"/", "0", true)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{
		"calendar-user-address-set", "mailto:" + testUser,
		"schedule-inbox-URL", "schedule-outbox-URL",
		"/dav/calendars/" + testUser + "/inbox/",
		"/dav/calendars/" + testUser + "/outbox/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("principal response missing %q\n%s", want, body)
		}
	}
}

// TestSchedulingCollections confirms the scheduling Inbox and Outbox report their
// own resourcetypes (RFC 6638 §2.1/§2.2) and are listed in the calendar home set.
func TestSchedulingCollections(t *testing.T) {
	ts := davServerCal(t)

	_, in := do(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/inbox/", "0", true)
	if !strings.Contains(in, "schedule-inbox") || strings.Contains(in, "<C:calendar/>") {
		t.Errorf("inbox PROPFIND lacks schedule-inbox resourcetype\n%s", in)
	}
	_, out := do(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/outbox/", "0", true)
	if !strings.Contains(out, "schedule-outbox") {
		t.Errorf("outbox PROPFIND lacks schedule-outbox resourcetype\n%s", out)
	}

	_, home := doFull(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/", "", map[string]string{"Depth": "1"})
	for _, want := range []string{"/inbox/", "/outbox/", "schedule-inbox", "schedule-outbox"} {
		if !strings.Contains(home, want) {
			t.Errorf("calendar home set missing %q\n%s", want, home)
		}
	}
}

// TestScheduleNameReserved confirms the reserved Inbox/Outbox segments cannot be
// created as user calendars and are never resolved as one (RFC 6638 §2.1/§2.2).
func TestScheduleNameReserved(t *testing.T) {
	ts := davServerCal(t)
	for _, name := range []string{"inbox", "outbox"} {
		path := "/dav/calendars/" + testUser + "/" + name + "/"
		// The reserved segment is a scheduling collection, so MKCALENDAR over it is
		// not allowed (405) — the client cannot create a user calendar that shadows it.
		if resp, _ := doFull(t, ts, "MKCALENDAR", path, "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("MKCALENDAR %s: status %d, want 405", name, resp.StatusCode)
		}
	}
	// A PUT addressing the reserved name as if it were a user calendar must not land
	// the object (the segment is a scheduling collection, not a calendar).
	if resp, _ := doFull(t, ts, "PUT", "/dav/calendars/"+testUser+"/inbox/x.ics", timedEventICS, nil); resp.StatusCode == http.StatusCreated {
		t.Errorf("PUT into the scheduling Inbox as a calendar succeeded (status %d); want rejection", resp.StatusCode)
	}
}

// TestQuotaProps confirms a collection PROPFIND reports quota-used-bytes always and
// quota-available-bytes only when a storage limit is set (RFC 4331).
func TestQuotaProps(t *testing.T) {
	// A mailbox with a storage limit reports both used and available.
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{StorageKB: 1024}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	_, body := do(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/calendar/", "0", true)
	for _, want := range []string{"quota-used-bytes", "quota-available-bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("limited mailbox PROPFIND missing %q\n%s", want, body)
		}
	}

	// An unlimited mailbox reports used but omits available.
	ts2 := davServerCal(t)
	_, body2 := do(t, ts2, "PROPFIND", "/dav/calendars/"+testUser+"/calendar/", "0", true)
	if !strings.Contains(body2, "quota-used-bytes") {
		t.Errorf("unlimited mailbox missing quota-used-bytes\n%s", body2)
	}
	if strings.Contains(body2, "quota-available-bytes") {
		t.Errorf("unlimited mailbox should omit quota-available-bytes\n%s", body2)
	}
}

// TestPrincipalPropertySearch confirms a principal-property-search REPORT matches
// the directory's users and returns each as an addressable principal (RFC 3744 §9.4).
func TestPrincipalPropertySearch(t *testing.T) {
	ts := davServerCal(t)
	body := `<D:principal-property-search xmlns:D="DAV:">` +
		`<D:property-search><D:prop><D:displayname/></D:prop><D:match>alice</D:match></D:property-search>` +
		`<D:prop><D:displayname/></D:prop></D:principal-property-search>`
	resp, out := doFull(t, ts, "REPORT", "/dav/principals/", body, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{"/dav/principals/" + testUser + "/", "mailto:" + testUser, "calendar-user-address-set"} {
		if !strings.Contains(out, want) {
			t.Errorf("principal search missing %q\n%s", want, out)
		}
	}
}

// TestPrincipalSearchPropertySet confirms the server advertises the properties a
// client may search principals on (RFC 3744 §9.5).
func TestPrincipalSearchPropertySet(t *testing.T) {
	ts := davServerCal(t)
	resp, out := doFull(t, ts, "REPORT", "/dav/principals/", `<D:principal-search-property-set xmlns:D="DAV:"/>`, map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{"principal-search-property-set", "principal-search-property", "displayname", "Display Name"} {
		if !strings.Contains(out, want) {
			t.Errorf("principal-search-property-set missing %q\n%s", want, out)
		}
	}
}

// TestCurrentUserPrivilegeSet confirms a collection PROPFIND reports the owner and
// the privileges the user holds, so a client can tell the calendar is writable
// (RFC 3744 §5.4).
func TestCurrentUserPrivilegeSet(t *testing.T) {
	ts := davServerCal(t)
	_, body := do(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/calendar/", "0", true)
	for _, want := range []string{
		"current-user-privilege-set", "write-content", "read-acl", "read-free-busy",
		"owner", "/dav/principals/" + testUser + "/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("calendar PROPFIND missing %q\n%s", want, body)
		}
	}
}
