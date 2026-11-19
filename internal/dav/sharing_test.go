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

// TestNonDelegateIsRefusedOnEveryMethodAndSpelling sends every method a client
// can aim at another account's collections, and spellings of that account's
// principal a path check could miss (upper case, a percent-encoded '@', a dot
// segment through the caller's own principal). Each must be refused, and the
// owner's event must survive the attempted DELETE and MOVE.
func TestNonDelegateIsRefusedOnEveryMethodAndSpelling(t *testing.T) {
	accs := directory.StaticAccounts{
		testUser:            {Password: testPass, MailboxPath: provisioned(t, "alice")},
		"carol@hermex.test": {Password: testPass, MailboxPath: provisioned(t, "carol")},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	event := "/dav/calendars/" + testUser + "/calendar/a.ics"
	ev := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:s1\r\nSUMMARY:S\r\n" +
		"DTSTART:20260701T140000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if code := authReq(t, ts, "PUT", event, testUser, ev); code != http.StatusCreated {
		t.Fatalf("owner PUT: status %d, want 201", code)
	}
	for _, c := range []struct{ method, path string }{
		{"DELETE", event},
		{"MOVE", event},
		{"PROPFIND", "/dav/calendars/" + testUser + "/calendar/"},
		{"REPORT", "/dav/calendars/" + testUser + "/calendar/"},
		{"GET", "/dav/addressbooks/" + testUser + "/contacts/"},
		{"PROPFIND", "/dav/addressbooks/" + testUser + "/contacts/"},
		{"PROPFIND", "/dav/principals/" + testUser + "/"},
		{"GET", "/dav/calendars/" + strings.ToUpper(testUser) + "/calendar/a.ics"},
		{"GET", "/dav/calendars/" + strings.Replace(testUser, "@", "%40", 1) + "/calendar/a.ics"},
		{"GET", "/dav/calendars/carol@hermex.test/../" + testUser + "/calendar/a.ics"},
	} {
		if code := authReq(t, ts, c.method, c.path, "carol@hermex.test", ""); code != http.StatusForbidden {
			t.Errorf("non-delegate %s %s: status %d, want 403", c.method, c.path, code)
		}
	}
	if code := authReq(t, ts, "GET", event, testUser, ""); code != http.StatusOK {
		t.Errorf("owner GET after the refused writes: status %d, want 200", code)
	}
}

// provisioned creates an empty mailbox store and returns its directory.
func provisioned(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return dir
}

// authBody performs an authenticated request and returns the response body.
func authBody(t *testing.T, ts *httptest.Server, method, path, user, depth string) string {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, testPass)
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// TestSharedMailboxInHomeSet confirms a shared mailbox the caller is delegated into
// appears in the caller's calendar home-set under the owner's principal href, and is
// absent for a non-delegate (#117 verify: a sharee sees the shared collection).
func TestSharedMailboxInHomeSet(t *testing.T) {
	teamDir := filepath.Join(t.TempDir(), "team")
	bobDir := filepath.Join(t.TempDir(), "bob")
	carolDir := filepath.Join(t.TempDir(), "carol")
	for _, d := range []string{teamDir, bobDir, carolDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	tst, err := objectstore.Open(teamDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := tst.SetDelegates([]string{"bob@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	tst.Close()

	accs := directory.StaticAccounts{
		"team@hermex.test":  {Password: testPass, MailboxPath: teamDir, Shared: true},
		"bob@hermex.test":   {Password: testPass, MailboxPath: bobDir},
		"carol@hermex.test": {Password: testPass, MailboxPath: carolDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)

	bobHome := authBody(t, ts, "PROPFIND", "/dav/calendars/bob@hermex.test/", "bob@hermex.test", "1")
	if !strings.Contains(bobHome, "/dav/calendars/team@hermex.test/calendar/") {
		t.Errorf("delegate's calendar home-set lacks the shared team calendar\n%s", bobHome)
	}

	carolHome := authBody(t, ts, "PROPFIND", "/dav/calendars/carol@hermex.test/", "carol@hermex.test", "1")
	if strings.Contains(carolHome, "team@hermex.test/calendar") {
		t.Errorf("non-delegate's home-set must not show the shared calendar\n%s", carolHome)
	}
}
