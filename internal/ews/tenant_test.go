package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// tenantServer serves alice with two calendar owners that granted her the same
// rights by name: bob in her own domain and dave in another one.
func tenantServer(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	paths := map[string]string{testUser: provisionedDir(t), "bob@hermex.test": provisionedDir(t), "dave@other.test": provisionedDir(t)}
	accs := directory.StaticAccounts{}
	for addr, p := range paths {
		accs[addr] = directory.Account{Password: testPass, MailboxPath: p}
	}
	for _, owner := range []string{"bob@hermex.test", "dave@other.test"} {
		grantFolder(t, paths[owner], int64(mapi.PrivateFIDCalendar), testUser, mapi.RightsReviewer|mapi.FrightsFreeBusyDetailed)
		start := time.Date(2026, 6, 19, 10, 15, 0, 0, time.UTC)
		seedAppointment(t, paths[owner], start, start.Add(30*time.Minute), 2, "Board meeting", false)
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, paths
}

// TestAnotherOrganizationsCalendarIsNotRead proves a folder grant speaks for the
// owner's organization only, as Exchange's permissions do: dave, in another
// domain, granted alice Reviewer and detailed free/busy by name, yet neither his
// availability nor his calendar folder is served to her, while bob's identical
// grant in her own domain is.
func TestAnotherOrganizationsCalendarIsNotRead(t *testing.T) {
	ts, _ := tenantServer(t)

	_, out := soapPost(t, ts, availabilityReq("dave@other.test", winStart, winEnd, true), true)
	if strings.Contains(out, "Board meeting") || strings.Contains(out, ">Busy</BusyType>") {
		t.Errorf("another organization's free/busy was served: %s", out)
	}
	_, out = soapPost(t, ts, availabilityReq("bob@hermex.test", winStart, winEnd, true), true)
	if !strings.Contains(out, "Board meeting") {
		t.Errorf("the same grant within the organization was not honoured: %s", out)
	}

	getCalendar := func(owner string) string {
		_, body := soapPost(t, ts, wrapRequest(`<GetFolder xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`">`+
			`<FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape><FolderIds>`+
			`<t:DistinguishedFolderId Id="calendar"><t:Mailbox><t:EmailAddress>`+owner+`</t:EmailAddress></t:Mailbox></t:DistinguishedFolderId>`+
			`</FolderIds></GetFolder>`), true)
		return body
	}
	if out := getCalendar("dave@other.test"); !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("another organization's calendar folder was opened: %s", out)
	}
	if out := getCalendar("bob@hermex.test"); !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("the calendar folder within the organization was refused: %s", out)
	}
}
