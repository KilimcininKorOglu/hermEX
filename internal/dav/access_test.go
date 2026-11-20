package dav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestSharedAccessFollowsFolderRights proves a request on another account's
// collections is decided by the folder rights the owner granted, as webmail2 and
// EWS decide it, and not by the owner's delegate list. The list is Exchange's
// send-on-behalf list ([MS-OXOSFLD]/PidTagPublicDelegates) and opens no folder.
//
// alice has an event and a contact. dave is only on her delegate list; rita holds
// Reviewer on her Calendar; ed holds Editor on her Calendar. Neither grant covers
// Contacts.
func TestSharedAccessFollowsFolderRights(t *testing.T) {
	aliceDir := provisioned(t, "alice")
	accs := directory.StaticAccounts{
		testUser:           {Password: testPass, MailboxPath: aliceDir},
		"dave@hermex.test": {Password: testPass, MailboxPath: provisioned(t, "dave")},
		"rita@hermex.test": {Password: testPass, MailboxPath: provisioned(t, "rita")},
		"ed@hermex.test":   {Password: testPass, MailboxPath: provisioned(t, "ed")},
	}
	st, err := objectstore.Open(aliceDir)
	mustOK(t, err)
	mustOK(t, st.SetDelegates([]string{"dave@hermex.test"}))
	mustOK(t, st.ModifyPermissions(int64(mapi.PrivateFIDCalendar), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "rita@hermex.test", Rights: mapi.NormalizeRights(mapi.RightsReviewer, true)},
		{Op: objectstore.PermAdd, Username: "ed@hermex.test", Rights: mapi.NormalizeRights(mapi.RightsEditor, true)},
	}))
	st.Close()

	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	cal := "/dav/calendars/" + testUser + "/calendar/"
	book := "/dav/addressbooks/" + testUser + "/contacts/"
	if code := authReq(t, ts, "PUT", cal+"a.ics", testUser, eventBody("a")); code != http.StatusCreated {
		t.Fatalf("owner PUT event: %d", code)
	}
	if code := putVCard(t, ts, book+"c.vcf", testUser); code != http.StatusCreated {
		t.Fatalf("owner PUT contact: %d", code)
	}

	const ok, no = true, false
	for _, c := range []struct {
		user, method, path, body string
		allowed                  bool
	}{
		// On the delegate list only: nothing opens.
		{"dave@hermex.test", "GET", cal + "a.ics", "", no},
		{"dave@hermex.test", "PUT", cal + "d.ics", eventBody("d"), no},
		{"dave@hermex.test", "PROPFIND", cal, "", no},
		// Reviewer on Calendar: reads it, writes nothing, sees no contacts.
		{"rita@hermex.test", "GET", cal + "a.ics", "", ok},
		{"rita@hermex.test", "PROPFIND", cal, "", ok},
		{"rita@hermex.test", "REPORT", cal, "", ok},
		{"rita@hermex.test", "PUT", cal + "r.ics", eventBody("r"), no},
		{"rita@hermex.test", "PUT", cal + "a.ics", eventBody("a"), no},
		{"rita@hermex.test", "DELETE", cal + "a.ics", "", no},
		{"rita@hermex.test", "PROPPATCH", cal, "", no},
		{"rita@hermex.test", "MKCALENDAR", "/dav/calendars/" + testUser + "/extra/", "", no},
		{"rita@hermex.test", "GET", book + "c.vcf", "", no},
		{"rita@hermex.test", "PROPFIND", book, "", no},
		{"rita@hermex.test", "POST", "/dav/calendars/" + testUser + "/outbox/", "", no},
		// Editor on Calendar: writes the calendar, still no contacts.
		{"ed@hermex.test", "PUT", cal + "e.ics", eventBody("e"), ok},
		{"ed@hermex.test", "DELETE", cal + "e.ics", "", ok},
		{"ed@hermex.test", "GET", book + "c.vcf", "", no},
		{"ed@hermex.test", "DELETE", book + "c.vcf", "", no},
	} {
		code := authReq(t, ts, c.method, c.path, c.user, c.body)
		if got := code != http.StatusForbidden; got != c.allowed {
			t.Errorf("%s %s %s: status %d, want allowed=%v", c.user, c.method, c.path, code, c.allowed)
		}
	}
	// MOVE needs the source deletable and the destination writable; Reviewer has neither.
	if code := moveReq(t, ts, cal+"a.ics", cal+"moved.ics", "rita@hermex.test"); code != http.StatusForbidden {
		t.Errorf("reviewer MOVE: status %d, want 403", code)
	}
	if code := moveReq(t, ts, cal+"a.ics", cal+"moved.ics", "ed@hermex.test"); code == http.StatusForbidden {
		t.Errorf("editor MOVE within the calendar: status %d, want it allowed", code)
	}
	if code := moveReq(t, ts, cal+"moved.ics", cal+"a.ics", "ed@hermex.test"); code == http.StatusForbidden {
		t.Errorf("editor MOVE back: status %d, want it allowed", code)
	}
	// The refused writes left the owner's objects in place.
	if code := authReq(t, ts, "GET", cal+"a.ics", testUser, ""); code != http.StatusOK {
		t.Errorf("owner event after the refused writes: status %d, want 200", code)
	}
	if code := authReq(t, ts, "GET", book+"c.vcf", testUser, ""); code != http.StatusOK {
		t.Errorf("owner contact after the refused writes: status %d, want 200", code)
	}
}

// TestSharedPropfindReportsTheGrant proves a read-only grant is reported as one
// (RFC 3744 5.4), so a client offers no edit the server would refuse, and a
// collection the caller cannot read is left out of the owner's home set.
func TestSharedPropfindReportsTheGrant(t *testing.T) {
	aliceDir := provisioned(t, "alice")
	accs := directory.StaticAccounts{
		testUser:           {Password: testPass, MailboxPath: aliceDir},
		"rita@hermex.test": {Password: testPass, MailboxPath: provisioned(t, "rita")},
	}
	st, err := objectstore.Open(aliceDir)
	mustOK(t, err)
	mustOK(t, st.ModifyPermissions(int64(mapi.PrivateFIDCalendar), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "rita@hermex.test", Rights: mapi.NormalizeRights(mapi.RightsReviewer, true)},
	}))
	st.Close()
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)

	body := authBody(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/calendar/", "rita@hermex.test", "0")
	if !strings.Contains(body, "current-user-privilege-set") || !strings.Contains(body, "<read ") {
		t.Fatalf("read-only grant reports no read privilege:\n%s", body)
	}
	for _, p := range []string{"<write-content ", "<bind ", "<unbind ", "<write-properties "} {
		if strings.Contains(body, p) {
			t.Errorf("read-only grant advertises %s:\n%s", p, body)
		}
	}
	home := authBody(t, ts, "PROPFIND", "/dav/calendars/"+testUser+"/", "rita@hermex.test", "1")
	if !strings.Contains(home, "/dav/calendars/"+testUser+"/calendar/") {
		t.Errorf("home set lacks the readable Calendar:\n%s", home)
	}
	if strings.Contains(home, "/dav/calendars/"+testUser+"/tasks/") {
		t.Errorf("home set lists Tasks, which grants the caller nothing:\n%s", home)
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func eventBody(uid string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:S\r\n" +
		"DTSTART:20260701T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func moveReq(t *testing.T, ts *httptest.Server, src, dst, user string) int {
	t.Helper()
	req, err := http.NewRequest("MOVE", ts.URL+src, nil)
	mustOK(t, err)
	req.SetBasicAuth(user, testPass)
	req.Header.Set("Destination", ts.URL+dst)
	resp, err := http.DefaultClient.Do(req)
	mustOK(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

func putVCard(t *testing.T, ts *httptest.Server, path, user string) int {
	t.Helper()
	req, err := http.NewRequest("PUT", ts.URL+path, strings.NewReader(adaVCard))
	mustOK(t, err)
	req.SetBasicAuth(user, testPass)
	req.Header.Set("Content-Type", "text/vcard")
	resp, err := http.DefaultClient.Do(req)
	mustOK(t, err)
	resp.Body.Close()
	return resp.StatusCode
}
