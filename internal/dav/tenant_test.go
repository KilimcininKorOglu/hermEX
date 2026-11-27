package dav

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestAnotherOrganizationsCalendarIsNotShared proves a calendar grant speaks for
// the owner's organization only: alice granted Editor and free/busy by name to
// rita in her domain and to omar in another one, and only rita reaches the
// calendar or its free/busy over CalDAV.
func TestAnotherOrganizationsCalendarIsNotShared(t *testing.T) {
	aliceDir := provisioned(t, "alice")
	accs := directory.StaticAccounts{
		testUser:           {Password: testPass, MailboxPath: aliceDir},
		"rita@hermex.test": {Password: testPass, MailboxPath: provisioned(t, "rita")},
		"omar@other.test":  {Password: testPass, MailboxPath: provisioned(t, "omar")},
	}
	st, err := objectstore.Open(aliceDir)
	mustOK(t, err)
	rights := mapi.NormalizeRights(mapi.RightsEditor|mapi.FrightsFreeBusyDetailed, true)
	mustOK(t, st.ModifyPermissions(int64(mapi.PrivateFIDCalendar), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "rita@hermex.test", Rights: rights},
		{Op: objectstore.PermAdd, Username: "omar@other.test", Rights: rights},
	}))
	mustClose(t, st)
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	cal := "/dav/calendars/" + testUser + "/calendar/"
	if code := authReq(t, ts, "PUT", cal+"a.ics", testUser, eventBody("a")); code != http.StatusCreated {
		t.Fatalf("owner PUT event: %d", code)
	}

	if code := authReq(t, ts, "GET", cal+"a.ics", "rita@hermex.test", ""); code != http.StatusOK {
		t.Errorf("a grant within the organization: GET status %d, want 200", code)
	}
	for _, m := range []string{"GET", "PROPFIND"} {
		if code := authReq(t, ts, m, cal+"a.ics", "omar@other.test", ""); code != http.StatusForbidden {
			t.Errorf("a grant across organizations: %s status %d, want 403", m, code)
		}
	}

	resp, out := doFull(t, ts, "POST", "/dav/calendars/omar@other.test/outbox/", freeBusyRequest("omar@other.test", testUser),
		map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("omar@other.test:"+testPass))})
	if resp.StatusCode != http.StatusOK || !strings.Contains(out, "3.8;No authority") {
		t.Errorf("free/busy across organizations: status %d\n%s", resp.StatusCode, out)
	}
}
