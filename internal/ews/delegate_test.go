package ews

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// delegateServer builds an EWS server over alice (the soapPost requester) and bob,
// returning their mailbox paths so a test can seed alice's delegate list and grants
// and exercise the foreign-mailbox guard against bob.
func delegateServer(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	paths := map[string]string{
		testUser:          t.TempDir(),
		"bob@hermex.test": t.TempDir(),
	}
	accs := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: paths[testUser]},
		"bob@hermex.test": {Password: testPass, MailboxPath: paths["bob@hermex.test"]},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, paths
}

// getDelegateReq builds a GetDelegate SOAP request for one mailbox. The element uses
// the real wire namespaces (messages default, types for EmailAddress) to confirm the
// namespace-agnostic request parser accepts a faithfully-namespaced request.
func getDelegateReq(mailbox string, includePerms bool) string {
	return wrapRequest(`<GetDelegate xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" IncludePermissions="` + strconv.FormatBool(includePerms) + `">` +
		`<Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></Mailbox>` +
		`</GetDelegate>`)
}

// setDelegateList records list as the mailbox's delegate list.
func setDelegateList(t *testing.T, path string, list []string) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetDelegates(list); err != nil {
		t.Fatal(err)
	}
}

// grantFolder grants username the given frights on a folder of the mailbox at path.
func grantFolder(t *testing.T, path string, fid int64, username string, rights uint32) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: username, Rights: rights},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestGetDelegateReportsListAndLevels confirms GetDelegate returns each delegate with
// the per-folder permission level its exact frights grant maps to (Editor/Reviewer),
// reports an ungranted folder as None, and always emits the meeting-message and
// private-item flags plus the delivery scope — the elements a strict client requires
// present even though v1 does not model their behaviour.
func TestGetDelegateReportsListAndLevels(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"delegate@hermex.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "delegate@hermex.test", mapi.RightsEditor)
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDInbox), "delegate@hermex.test", mapi.RightsReviewer)

	_, out := soapPost(t, ts, getDelegateReq(testUser, true), true)

	for _, want := range []string{
		`ResponseClass="Success"`,
		"PrimarySmtpAddress>delegate@hermex.test<",
		"CalendarFolderPermissionLevel>Editor<",
		"InboxFolderPermissionLevel>Reviewer<",
		"TasksFolderPermissionLevel>None<",
		"ContactsFolderPermissionLevel>None<",
		"ReceiveCopiesOfMeetingMessages>false<",
		"ViewPrivateItems>false<",
		"DeliverMeetingRequests>DelegatesAndMe<",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("GetDelegate response missing %q:\n%s", want, out)
		}
	}
}

// TestGetDelegateCustomForNonRoleMask confirms a folder grant whose mask is not an
// exact canonical role is reported as Custom, never snapped to the nearest named
// level. Reporting "Reviewer" or "Editor" for a hand-built mask would silently widen
// the grant on a client's read-modify-write cycle.
func TestGetDelegateCustomForNonRoleMask(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"delegate@hermex.test"})
	// ReadAny|Create is not any canonical role's exact mask.
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "delegate@hermex.test", mapi.FrightsReadAny|mapi.FrightsCreate)

	_, out := soapPost(t, ts, getDelegateReq(testUser, true), true)

	if !strings.Contains(out, "CalendarFolderPermissionLevel>Custom<") {
		t.Errorf("a non-role frights mask must report Custom:\n%s", out)
	}
}

// TestGetDelegateRoleWithFreeBusyStillRole confirms a role grant that also carries
// the ambient free/busy bits (which every mailbox seeds by default) still reports as
// the role, not Custom. The free/busy bits are orthogonal to the role and must not
// push an Editor grant into Custom just because the mailbox shares free/busy.
func TestGetDelegateRoleWithFreeBusyStillRole(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"delegate@hermex.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "delegate@hermex.test",
		mapi.RightsEditor|mapi.FrightsFreeBusyDetailed|mapi.FrightsFreeBusySimple)

	_, out := soapPost(t, ts, getDelegateReq(testUser, true), true)

	if !strings.Contains(out, "CalendarFolderPermissionLevel>Editor<") {
		t.Errorf("a role grant carrying free/busy bits must still report the role:\n%s", out)
	}
}

// TestGetDelegateCaseInsensitiveJoin confirms a delegate is paired with its folder
// grant even when the delegate-list entry and the permission row are stored in
// different case. The list entry is the join key the ROP logon also matches
// case-insensitively; reporting None here would mean GetDelegate and the ROP
// enforcement path disagree about the same delegate's access.
func TestGetDelegateCaseInsensitiveJoin(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"Delegate@HermEX.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "delegate@hermex.test", mapi.RightsEditor)

	_, out := soapPost(t, ts, getDelegateReq(testUser, true), true)

	if !strings.Contains(out, "PrimarySmtpAddress>Delegate@HermEX.test<") {
		t.Errorf("delegate list entry must be reported as stored:\n%s", out)
	}
	if !strings.Contains(out, "CalendarFolderPermissionLevel>Editor<") {
		t.Errorf("a case-differing permission row must still pair with the list entry:\n%s", out)
	}
}

// TestGetDelegateOmitsPermissionsWhenNotRequested confirms IncludePermissions=false
// returns the delegate identity without a DelegatePermissions block.
func TestGetDelegateOmitsPermissionsWhenNotRequested(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"delegate@hermex.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "delegate@hermex.test", mapi.RightsEditor)

	_, out := soapPost(t, ts, getDelegateReq(testUser, false), true)

	if !strings.Contains(out, "PrimarySmtpAddress>delegate@hermex.test<") {
		t.Errorf("delegate identity must still be reported:\n%s", out)
	}
	if strings.Contains(out, "DelegatePermissions") {
		t.Errorf("IncludePermissions=false must omit the permission block:\n%s", out)
	}
}

// TestGetDelegateForeignMailboxDenied confirms v1 refuses to serve another mailbox's
// delegate list: a request naming bob (a different, resolvable mailbox) is denied
// rather than silently returning alice's own delegates.
func TestGetDelegateForeignMailboxDenied(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"delegate@hermex.test"})

	_, out := soapPost(t, ts, getDelegateReq("bob@hermex.test", true), true)

	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("serving a foreign mailbox's delegates must be denied:\n%s", out)
	}
	if strings.Contains(out, "delegate@hermex.test") {
		t.Errorf("a denied request must not leak the caller's own delegate list:\n%s", out)
	}
}
