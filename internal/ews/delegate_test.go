package ews

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
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

// TestGetDelegateUserIdsFilter confirms a UserIds filter narrows the result to the
// named delegates and reports a requested-but-absent delegate as ErrorDelegateNotFound,
// instead of returning the whole list.
func TestGetDelegateUserIdsFilter(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"a@hermex.test", "b@hermex.test"})

	body := wrapRequest(`<GetDelegate xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" IncludePermissions="false">` +
		`<Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></Mailbox>` +
		`<UserIds><t:UserId><t:PrimarySmtpAddress>a@hermex.test</t:PrimarySmtpAddress></t:UserId>` +
		`<t:UserId><t:PrimarySmtpAddress>missing@hermex.test</t:PrimarySmtpAddress></t:UserId></UserIds>` +
		`</GetDelegate>`)
	_, out := soapPost(t, ts, body, true)

	if !strings.Contains(out, "PrimarySmtpAddress>a@hermex.test<") {
		t.Errorf("requested delegate must be returned:\n%s", out)
	}
	if strings.Contains(out, "PrimarySmtpAddress>b@hermex.test<") {
		t.Errorf("unrequested delegate must be filtered out:\n%s", out)
	}
	if !strings.Contains(out, "ErrorDelegateNotFound") || !strings.Contains(out, "PrimarySmtpAddress>missing@hermex.test<") {
		t.Errorf("requested-but-absent delegate must report ErrorDelegateNotFound:\n%s", out)
	}
}

// mutateDelegateReq builds an AddDelegate/UpdateDelegate request for one delegate with
// the given per-folder levels (folder name -> level). A nil levels map omits the
// DelegatePermissions block entirely.
func mutateDelegateReq(op, mailbox, delegate string, levels map[string]string) string {
	perms := ""
	if levels != nil {
		perms = "<t:DelegatePermissions>"
		for _, f := range []string{"Calendar", "Tasks", "Inbox", "Contacts", "Notes", "Journal"} {
			if lv, ok := levels[f]; ok {
				perms += "<t:" + f + "FolderPermissionLevel>" + lv + "</t:" + f + "FolderPermissionLevel>"
			}
		}
		perms += "</t:DelegatePermissions>"
	}
	return wrapRequest(`<` + op + ` xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></Mailbox>` +
		`<DelegateUsers><t:DelegateUser><t:UserId><t:PrimarySmtpAddress>` + delegate + `</t:PrimarySmtpAddress></t:UserId>` +
		perms +
		`</t:DelegateUser></DelegateUsers>` +
		`</` + op + `>`)
}

func removeDelegateReq(mailbox, delegate string) string {
	return wrapRequest(`<RemoveDelegate xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></Mailbox>` +
		`<UserIds><t:UserId><t:PrimarySmtpAddress>` + delegate + `</t:PrimarySmtpAddress></t:UserId></UserIds>` +
		`</RemoveDelegate>`)
}

// resolveGrant reads a user's effective rights on a folder of the mailbox at path.
func resolveGrant(t *testing.T, path string, fid int64, username string) uint32 {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, err := st.ResolvePermission(fid, username)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ownFolderGrant returns a user's OWN explicit grant on a folder and whether such a
// row exists — distinct from ResolvePermission, which falls back to the seeded
// "default" free/busy grant when the user has no row of their own.
func ownFolderGrant(t *testing.T, path string, fid int64, username string) (uint32, bool) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, err := st.ListPermissions(fid)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.MemberID > 0 && strings.EqualFold(e.Name, username) {
			return e.Rights, true
		}
	}
	return 0, false
}

// delegateListOf returns the mailbox's delegate list.
func delegateListOf(t *testing.T, path string) []string {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, err := st.GetDelegates()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// TestAddDelegateCreatesListAndGrants confirms AddDelegate records the delegate on the
// list and writes the requested per-folder grants to the mailbox ACLs.
func TestAddDelegateCreatesListAndGrants(t *testing.T) {
	ts, paths := delegateServer(t)

	_, out := soapPost(t, ts, mutateDelegateReq("AddDelegate", testUser, "del@hermex.test",
		map[string]string{"Calendar": "Editor", "Inbox": "Reviewer"}), true)
	if !strings.Contains(out, `ResponseClass="Success"`) || !strings.Contains(out, "PrimarySmtpAddress>del@hermex.test<") {
		t.Fatalf("AddDelegate must succeed for the delegate:\n%s", out)
	}

	if !containsFold(delegateListOf(t, paths[testUser]), "del@hermex.test") {
		t.Errorf("delegate must be recorded on the list")
	}
	if r := resolveGrant(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test"); r&mapi.FrightsEditAny == 0 {
		t.Errorf("Calendar Editor grant must carry edit-any, got %#x", r)
	}
	if r := resolveGrant(t, paths[testUser], int64(mapi.PrivateFIDInbox), "del@hermex.test"); r&mapi.FrightsReadAny == 0 || r&mapi.FrightsEditAny != 0 {
		t.Errorf("Inbox Reviewer grant must be read-only, got %#x", r)
	}
}

// TestAddDelegateClosesRopJoinAcrossCase is the cross-protocol proof: a delegate added
// via EWS under one case must be seen by the ROP enforcement path under another. The
// ROP path reads exactly GetDelegates (case-folded) and ResolvePermission (NOCASE
// column), so querying those with a differently-cased address proves EWS writes and
// ROP reads agree on the same delegate's identity and access.
func TestAddDelegateClosesRopJoinAcrossCase(t *testing.T) {
	ts, paths := delegateServer(t)

	_, out := soapPost(t, ts, mutateDelegateReq("AddDelegate", testUser, "Delegate@HermEX.test",
		map[string]string{"Calendar": "Editor"}), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("AddDelegate must succeed:\n%s", out)
	}

	// The ROP logon presents the authenticated login, which may differ in case.
	if !containsFold(delegateListOf(t, paths[testUser]), "delegate@hermex.test") {
		t.Errorf("delegate list must match the ROP caller case-insensitively")
	}
	if r := resolveGrant(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "delegate@hermex.test"); r&mapi.FrightsEditAny == 0 {
		t.Errorf("folder grant must resolve for the lowercased login the ROP path presents, got %#x", r)
	}
}

// TestAddDelegateDuplicateRejected confirms re-adding an existing delegate reports
// ErrorDelegateAlreadyExists rather than duplicating the list entry.
func TestAddDelegateDuplicateRejected(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"del@hermex.test"})

	_, out := soapPost(t, ts, mutateDelegateReq("AddDelegate", testUser, "del@hermex.test", nil), true)
	if !strings.Contains(out, "ErrorDelegateAlreadyExists") {
		t.Errorf("re-adding an existing delegate must be rejected:\n%s", out)
	}
	if list := delegateListOf(t, paths[testUser]); len(list) != 1 {
		t.Errorf("the list must not gain a duplicate entry, got %v", list)
	}
}

// TestAddDelegateForeignMailboxDenied confirms AddDelegate refuses another mailbox and
// writes nothing.
func TestAddDelegateForeignMailboxDenied(t *testing.T) {
	ts, paths := delegateServer(t)

	_, out := soapPost(t, ts, mutateDelegateReq("AddDelegate", "bob@hermex.test", "del@hermex.test",
		map[string]string{"Calendar": "Editor"}), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("managing a foreign mailbox must be denied:\n%s", out)
	}
	if list := delegateListOf(t, paths["bob@hermex.test"]); len(list) != 0 {
		t.Errorf("a denied request must not write to the target mailbox, got %v", list)
	}
}

// TestRemoveDelegateDropsListKeepsGrants confirms RemoveDelegate removes the list
// entry but leaves any explicit folder grant in place (an independent share, not part
// of the delegate designation).
func TestRemoveDelegateDropsListKeepsGrants(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"del@hermex.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test", mapi.RightsEditor)

	_, out := soapPost(t, ts, removeDelegateReq(testUser, "del@hermex.test"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("RemoveDelegate must succeed:\n%s", out)
	}
	if containsFold(delegateListOf(t, paths[testUser]), "del@hermex.test") {
		t.Errorf("delegate must be removed from the list")
	}
	if r := resolveGrant(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test"); r&mapi.FrightsEditAny == 0 {
		t.Errorf("an explicit folder grant must survive delegate removal, got %#x", r)
	}
}

// TestRemoveDelegateNotFound confirms removing a non-member reports ErrorDelegateNotFound.
func TestRemoveDelegateNotFound(t *testing.T) {
	ts, paths := delegateServer(t)
	_ = paths

	_, out := soapPost(t, ts, removeDelegateReq(testUser, "nobody@hermex.test"), true)
	if !strings.Contains(out, "ErrorDelegateNotFound") {
		t.Errorf("removing a non-member must report ErrorDelegateNotFound:\n%s", out)
	}
}

// TestUpdateDelegateChangesGrants confirms UpdateDelegate rewrites an existing
// delegate's folder grant (upsert) without changing list membership.
func TestUpdateDelegateChangesGrants(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"del@hermex.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test", mapi.RightsReviewer)

	_, out := soapPost(t, ts, mutateDelegateReq("UpdateDelegate", testUser, "del@hermex.test",
		map[string]string{"Calendar": "Editor"}), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateDelegate must succeed:\n%s", out)
	}
	if r := resolveGrant(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test"); r&mapi.FrightsEditAny == 0 {
		t.Errorf("Calendar grant must be raised to Editor, got %#x", r)
	}
	if list := delegateListOf(t, paths[testUser]); len(list) != 1 {
		t.Errorf("UpdateDelegate must not change list membership, got %v", list)
	}
}

// TestUpdateDelegateNotFound confirms updating a non-member reports ErrorDelegateNotFound
// and writes no grant.
func TestUpdateDelegateNotFound(t *testing.T) {
	ts, paths := delegateServer(t)

	_, out := soapPost(t, ts, mutateDelegateReq("UpdateDelegate", testUser, "ghost@hermex.test",
		map[string]string{"Calendar": "Editor"}), true)
	if !strings.Contains(out, "ErrorDelegateNotFound") {
		t.Errorf("updating a non-member must report ErrorDelegateNotFound:\n%s", out)
	}
	if _, found := ownFolderGrant(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "ghost@hermex.test"); found {
		t.Errorf("no own grant row must be written for a non-member")
	}
}

// TestUpdateDelegateNoneClearsGrant confirms a level of None clears the delegate's
// grant on that folder.
func TestUpdateDelegateNoneClearsGrant(t *testing.T) {
	ts, paths := delegateServer(t)
	setDelegateList(t, paths[testUser], []string{"del@hermex.test"})
	grantFolder(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test", mapi.RightsEditor)

	_, out := soapPost(t, ts, mutateDelegateReq("UpdateDelegate", testUser, "del@hermex.test",
		map[string]string{"Calendar": "None"}), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("UpdateDelegate must succeed:\n%s", out)
	}
	if r := resolveGrant(t, paths[testUser], int64(mapi.PrivateFIDCalendar), "del@hermex.test"); r != 0 {
		t.Errorf("a None level must clear the grant, got %#x", r)
	}
}

// TestAddThenGetDelegateRoundTrip confirms a grant written by AddDelegate reads back at
// the same level through GetDelegate — the two EWS operations agree end to end.
func TestAddThenGetDelegateRoundTrip(t *testing.T) {
	ts, _ := delegateServer(t)

	soapPost(t, ts, mutateDelegateReq("AddDelegate", testUser, "del@hermex.test",
		map[string]string{"Calendar": "Editor", "Tasks": "Author"}), true)

	_, out := soapPost(t, ts, getDelegateReq(testUser, true), true)
	if !strings.Contains(out, "PrimarySmtpAddress>del@hermex.test<") {
		t.Errorf("GetDelegate must list the added delegate:\n%s", out)
	}
	if !strings.Contains(out, "CalendarFolderPermissionLevel>Editor<") {
		t.Errorf("Calendar Editor must round-trip:\n%s", out)
	}
	if !strings.Contains(out, "TasksFolderPermissionLevel>Author<") {
		t.Errorf("Tasks Author must round-trip:\n%s", out)
	}
}

// crossMailboxGetFolder builds a GetFolder request for a distinguished folder, targeting
// another mailbox when mailbox is non-empty.
func crossMailboxGetFolder(distinguishedID, mailbox string) string {
	mb := ""
	if mailbox != "" {
		mb = `<t:Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></t:Mailbox>`
	}
	return wrapRequest(`<GetFolder xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<FolderShape><t:BaseShape>IdOnly</t:BaseShape></FolderShape>` +
		`<FolderIds><t:DistinguishedFolderId Id="` + distinguishedID + `">` + mb + `</t:DistinguishedFolderId></FolderIds>` +
		`</GetFolder>`)
}

// TestGetFolderCrossMailboxWithGrant confirms a caller granted access on another
// mailbox's folder reads that folder through a Mailbox-targeted GetFolder — the EWS
// access path honoring the same grant the ROP path does.
func TestGetFolderCrossMailboxWithGrant(t *testing.T) {
	ts, paths := delegateServer(t)
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox), testUser, mapi.RightsReviewer)

	_, out := soapPost(t, ts, crossMailboxGetFolder("inbox", "bob@hermex.test"), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("a granted caller must read the target folder:\n%s", out)
	}
}

// TestGetFolderCrossMailboxDeniedWithoutGrant confirms a caller with no permission on
// another mailbox's folder is denied, not silently served. The inbox carries no
// default grant, so visibility comes only from an explicit grant.
func TestGetFolderCrossMailboxDeniedWithoutGrant(t *testing.T) {
	ts, _ := delegateServer(t)

	_, out := soapPost(t, ts, crossMailboxGetFolder("inbox", "bob@hermex.test"), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("an ungranted caller must be denied the target folder:\n%s", out)
	}
}

// TestGetFolderCrossMailboxUnknownMailbox confirms targeting a non-existent mailbox
// reports ErrorNonExistentMailbox.
func TestGetFolderCrossMailboxUnknownMailbox(t *testing.T) {
	ts, _ := delegateServer(t)

	_, out := soapPost(t, ts, crossMailboxGetFolder("inbox", "ghost@nowhere.test"), true)
	if !strings.Contains(out, "ErrorNonExistentMailbox") {
		t.Errorf("an unknown target mailbox must report ErrorNonExistentMailbox:\n%s", out)
	}
}

// TestGetFolderOwnMailboxViaMailboxElement confirms a Mailbox naming the caller's own
// mailbox is served without delegate enforcement (the owner path is unchanged).
func TestGetFolderOwnMailboxViaMailboxElement(t *testing.T) {
	ts, _ := delegateServer(t)

	_, out := soapPost(t, ts, crossMailboxGetFolder("inbox", testUser), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("the caller's own mailbox must be served without a grant:\n%s", out)
	}
}

// crossMailboxFindItem builds a FindItem request whose parent folder targets another
// mailbox when mailbox is non-empty.
func crossMailboxFindItem(distinguishedID, mailbox string) string {
	mb := ""
	if mailbox != "" {
		mb = `<t:Mailbox><t:EmailAddress>` + mailbox + `</t:EmailAddress></t:Mailbox>`
	}
	return wrapRequest(`<FindItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" Traversal="Shallow">` +
		`<ItemShape><t:BaseShape>IdOnly</t:BaseShape></ItemShape>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="` + distinguishedID + `">` + mb + `</t:DistinguishedFolderId></ParentFolderIds>` +
		`</FindItem>`)
}

// firstItemID extracts the first ItemId Id attribute value from a SOAP response.
func firstItemID(out string) string {
	const marker = `ItemId Id="`
	i := strings.Index(out, marker)
	if i < 0 {
		return ""
	}
	rest := out[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// seedInboxMessage appends one message with the given subject to a mailbox's inbox,
// indexing it so FindItem/ListMessages enumerate it (the IMAP-index delivery path,
// unlike CreateMessage which writes only the object store).
func seedInboxMessage(t *testing.T, path, subject string) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: sender@example.test\r\nSubject: " + subject + "\r\n\r\nbody\r\n"
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0); err != nil {
		t.Fatal(err)
	}
}

// TestFindItemCrossMailboxWithGrant confirms a caller granted visibility on another
// mailbox's folder can list its items, and that each returned item id encodes the
// target mailbox so a later request reopens it.
func TestFindItemCrossMailboxWithGrant(t *testing.T) {
	ts, paths := delegateServer(t)
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox), testUser, mapi.RightsReviewer)
	seedInboxMessage(t, paths["bob@hermex.test"], "Quarterly numbers")

	_, out := soapPost(t, ts, crossMailboxFindItem("inbox", "bob@hermex.test"), true)
	if !strings.Contains(out, "Quarterly numbers") {
		t.Fatalf("a granted caller must list the target's items:\n%s", out)
	}
	id := firstItemID(out)
	dec, err := oxews.DecodeItemID(id)
	if err != nil || !strings.EqualFold(dec.Mailbox, "bob@hermex.test") {
		t.Errorf("the returned item id must encode the target mailbox, got %q (%v)", dec.Mailbox, err)
	}
}

// TestFindItemCrossMailboxDenied confirms a caller with no visibility on another
// mailbox's folder cannot list it.
func TestFindItemCrossMailboxDenied(t *testing.T) {
	ts, paths := delegateServer(t)
	seedInboxMessage(t, paths["bob@hermex.test"], "secret")

	_, out := soapPost(t, ts, crossMailboxFindItem("inbox", "bob@hermex.test"), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("an ungranted caller must be denied the listing:\n%s", out)
	}
}

// TestCrossMailboxFindThenGetItem confirms the full chain: a granted caller lists
// another mailbox's item and then fetches it by the returned id, which reopens the
// target mailbox and passes the read gate.
func TestCrossMailboxFindThenGetItem(t *testing.T) {
	ts, paths := delegateServer(t)
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox), testUser, mapi.RightsReviewer)
	seedInboxMessage(t, paths["bob@hermex.test"], "Quarterly numbers")

	_, findOut := soapPost(t, ts, crossMailboxFindItem("inbox", "bob@hermex.test"), true)
	id := firstItemID(findOut)
	if id == "" {
		t.Fatalf("no item id returned:\n%s", findOut)
	}
	_, getOut := soapPost(t, ts, getItemReq(id), true)
	if !strings.Contains(getOut, `ResponseClass="Success"`) {
		t.Errorf("GetItem on the cross-mailbox id must succeed:\n%s", getOut)
	}
}

// TestCrossMailboxTwoTierVisibleListsButReadDenied confirms the two-tier gate: a
// visibility-only grant lets the caller LIST the folder (FindItem) but not READ an
// item's content (GetItem), which requires read access. This is the exact distinction
// the EWS enforcement contract draws between folder visibility and item read.
func TestCrossMailboxTwoTierVisibleListsButReadDenied(t *testing.T) {
	ts, paths := delegateServer(t)
	// A visibility-only grant: the folder is listable but its items are not readable.
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox), testUser, mapi.FrightsVisible)
	seedInboxMessage(t, paths["bob@hermex.test"], "Quarterly numbers")

	_, findOut := soapPost(t, ts, crossMailboxFindItem("inbox", "bob@hermex.test"), true)
	if !strings.Contains(findOut, "Quarterly numbers") {
		t.Fatalf("visibility must allow listing:\n%s", findOut)
	}
	id := firstItemID(findOut)
	_, getOut := soapPost(t, ts, getItemReq(id), true)
	if !strings.Contains(getOut, "ErrorAccessDenied") {
		t.Errorf("reading an item without read access must be denied:\n%s", getOut)
	}
}

// bobItemIDWithGrant grants the right on bob's inbox, seeds one message, lists it, and
// returns its mailbox-encoded item id (the cross-mailbox id a write op then targets).
func bobItemIDWithGrant(t *testing.T, ts *httptest.Server, paths map[string]string, right uint32) string {
	t.Helper()
	grantFolder(t, paths["bob@hermex.test"], int64(mapi.PrivateFIDInbox), testUser, right)
	seedInboxMessage(t, paths["bob@hermex.test"], "Quarterly numbers")
	_, out := soapPost(t, ts, crossMailboxFindItem("inbox", "bob@hermex.test"), true)
	id := firstItemID(out)
	if id == "" {
		t.Fatalf("no item id from FindItem:\n%s", out)
	}
	return id
}

func updateItemReadReq(itemID string) string {
	return wrapRequest(`<UpdateItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" ConflictResolution="AlwaysOverwrite">` +
		`<ItemChanges><t:ItemChange><t:ItemId Id="` + itemID + `"/>` +
		`<t:Updates><t:SetItemField><t:FieldURI FieldURI="message:IsRead"/>` +
		`<t:Message><t:IsRead>true</t:IsRead></t:Message></t:SetItemField></t:Updates>` +
		`</t:ItemChange></ItemChanges></UpdateItem>`)
}

func deleteItemHardReq(itemID string) string {
	return wrapRequest(`<DeleteItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" DeleteType="HardDelete">` +
		`<ItemIds><t:ItemId Id="` + itemID + `"/></ItemIds>` +
		`</DeleteItem>`)
}

func moveItemReq(itemID, destDistinguished, destMailbox string) string {
	mb := ""
	if destMailbox != "" {
		mb = `<t:Mailbox><t:EmailAddress>` + destMailbox + `</t:EmailAddress></t:Mailbox>`
	}
	return wrapRequest(`<MoveItem xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ToFolderId><t:DistinguishedFolderId Id="` + destDistinguished + `">` + mb + `</t:DistinguishedFolderId></ToFolderId>` +
		`<ItemIds><t:ItemId Id="` + itemID + `"/></ItemIds>` +
		`</MoveItem>`)
}

// TestUpdateItemCrossMailboxWithGrant confirms an editor-rights delegate can update an
// item in another mailbox.
func TestUpdateItemCrossMailboxWithGrant(t *testing.T) {
	ts, paths := delegateServer(t)
	id := bobItemIDWithGrant(t, ts, paths, mapi.RightsEditor)

	_, out := soapPost(t, ts, updateItemReadReq(id), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("an editor delegate must update the target item:\n%s", out)
	}
}

// TestUpdateItemCrossMailboxDenied confirms a read-only (reviewer) delegate cannot edit
// an item in another mailbox — editing needs edit access, which visibility/read lack.
func TestUpdateItemCrossMailboxDenied(t *testing.T) {
	ts, paths := delegateServer(t)
	id := bobItemIDWithGrant(t, ts, paths, mapi.RightsReviewer)

	_, out := soapPost(t, ts, updateItemReadReq(id), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("a reviewer delegate must not edit the target item:\n%s", out)
	}
}

// TestDeleteItemCrossMailboxWithGrant confirms an editor-rights delegate can delete an
// item in another mailbox.
func TestDeleteItemCrossMailboxWithGrant(t *testing.T) {
	ts, paths := delegateServer(t)
	id := bobItemIDWithGrant(t, ts, paths, mapi.RightsEditor)

	_, out := soapPost(t, ts, deleteItemHardReq(id), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("an editor delegate must delete the target item:\n%s", out)
	}
}

// TestDeleteItemCrossMailboxDenied confirms a reviewer delegate cannot delete an item
// in another mailbox.
func TestDeleteItemCrossMailboxDenied(t *testing.T) {
	ts, paths := delegateServer(t)
	id := bobItemIDWithGrant(t, ts, paths, mapi.RightsReviewer)

	_, out := soapPost(t, ts, deleteItemHardReq(id), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("a reviewer delegate must not delete the target item:\n%s", out)
	}
}

// TestMoveItemCrossStoreRejected confirms moving an item from another mailbox into the
// caller's own mailbox is refused: the copy runs within a single store, so a move that
// would span mailboxes (and could misapply a foreign id) is rejected.
func TestMoveItemCrossStoreRejected(t *testing.T) {
	ts, paths := delegateServer(t)
	id := bobItemIDWithGrant(t, ts, paths, mapi.RightsEditor)

	// Destination is the caller's own deleted-items (no Mailbox element).
	_, out := soapPost(t, ts, moveItemReq(id, "deleteditems", ""), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("a cross-mailbox move must be rejected:\n%s", out)
	}
}

// TestCreateAttachmentCrossMailboxRejected confirms the safety guard: an item-id-driven
// op not yet wired for cross-mailbox refuses a foreign id rather than misapplying it to
// the caller's own store.
func TestCreateAttachmentCrossMailboxRejected(t *testing.T) {
	ts, _ := delegateServer(t)
	foreign := oxews.EncodeItemID(oxews.ItemID{
		FolderID: int64(mapi.PrivateFIDInbox), MessageID: 1, UID: 1, Mailbox: "bob@hermex.test",
	})

	_, out := soapPost(t, ts, createAttachmentReq(foreign, "note.txt", "text/plain", "aGk="), true)
	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("a foreign parent id must be rejected:\n%s", out)
	}
}
