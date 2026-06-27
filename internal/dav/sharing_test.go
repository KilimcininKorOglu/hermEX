package dav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// authReq performs an authenticated DAV request and returns the status code.
func authReq(t *testing.T, ts *httptest.Server, method, path, user, body string) int {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, testPass)
	if body != "" {
		req.Header.Set("Content-Type", "text/calendar")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestSharedCalendarDelegateAccess confirms collection sharing routes a delegate's
// request for an owner's principal to the owner's mailbox, while a non-delegate is
// forbidden (the OWASP A01 broken-access-control guard, #117). alice grants only bob a
// delegate; bob reads/writes alice's calendar, carol cannot, and the write lands in
// alice's store rather than bob's own.
func TestSharedCalendarDelegateAccess(t *testing.T) {
	aliceDir := filepath.Join(t.TempDir(), "alice")
	bobDir := filepath.Join(t.TempDir(), "bob")
	carolDir := filepath.Join(t.TempDir(), "carol")
	for _, d := range []string{aliceDir, bobDir, carolDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	// alice grants bob (only) delegate access to her mailbox.
	ast, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ast.SetDelegates([]string{"bob@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	ast.Close()

	accs := directory.StaticAccounts{
		testUser:            {Password: testPass, MailboxPath: aliceDir},
		"bob@hermex.test":   {Password: testPass, MailboxPath: bobDir},
		"carol@hermex.test": {Password: testPass, MailboxPath: carolDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)

	aliceCal := "/dav/calendars/" + testUser + "/calendar/shared.ics"
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:s1\r\nSUMMARY:Shared\r\n" +
		"DTSTART:20260701T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	// bob (a delegate) writes into and reads from alice's calendar.
	if code := authReq(t, ts, "PUT", aliceCal, "bob@hermex.test", ev); code != http.StatusCreated {
		t.Fatalf("delegate PUT to owner calendar: status %d, want 201", code)
	}
	if code := authReq(t, ts, "GET", aliceCal, "bob@hermex.test", ""); code != http.StatusOK {
		t.Errorf("delegate GET from owner calendar: status %d, want 200", code)
	}

	// carol (NOT a delegate) is forbidden on alice's principal -- the access-control guard.
	if code := authReq(t, ts, "GET", aliceCal, "carol@hermex.test", ""); code != http.StatusForbidden {
		t.Errorf("non-delegate GET on owner principal: status %d, want 403", code)
	}
	if code := authReq(t, ts, "PUT", "/dav/calendars/"+testUser+"/calendar/x.ics", "carol@hermex.test", ev); code != http.StatusForbidden {
		t.Errorf("non-delegate PUT on owner principal: status %d, want 403", code)
	}

	// The write landed in ALICE's store, not bob's own: bob's own calendar lacks it.
	if code := authReq(t, ts, "GET", "/dav/calendars/bob@hermex.test/calendar/shared.ics", "bob@hermex.test", ""); code != http.StatusNotFound {
		t.Errorf("shared write must land in the owner store, not the delegate's: status %d, want 404", code)
	}
}
