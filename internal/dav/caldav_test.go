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
