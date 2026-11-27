package webmail2api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestAclSanitizeRightsDropsForbiddenBits proves the ingest mask strips any bit
// outside RightsMaxROP (reserved / reference-private extensions) while preserving
// every standard named-level preset the SPA sends.
func TestAclSanitizeRightsDropsForbiddenBits(t *testing.T) {
	if got := aclSanitizeRights(0xFFFFFFFF); got != mapi.RightsMaxROP {
		t.Errorf("sanitize(all bits) = %#x, want RightsMaxROP %#x", got, mapi.RightsMaxROP)
	}
	for _, preset := range []uint32{
		mapi.RightsReviewer, mapi.RightsContributor, mapi.RightsNoneditingAuthor,
		mapi.RightsAuthor, mapi.RightsPublishingAuthor, mapi.RightsEditor,
		mapi.RightsPublishingEditor, mapi.RightsOwner, mapi.RightsNone,
	} {
		if got := aclSanitizeRights(preset); got != preset {
			t.Errorf("sanitize(%#x) = %#x, want the preset unchanged", preset, got)
		}
	}
}

func TestFolderDescendants(t *testing.T) {
	pid := func(v int64) *int64 { return &v }
	// tree: 1 -> {2 -> {4}, 3}, 5 (unrelated)
	folders := []objectstore.FolderInfo{
		{ID: 1, ParentID: nil},
		{ID: 2, ParentID: pid(1)},
		{ID: 3, ParentID: pid(1)},
		{ID: 4, ParentID: pid(2)},
		{ID: 5, ParentID: nil},
	}
	got := folderDescendants(folders, 1)
	want := map[int64]bool{2: true, 3: true, 4: true}
	if len(got) != len(want) {
		t.Fatalf("descendants(1) = %v, want ids %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("descendants(1) contains unexpected id %d", id)
		}
	}
	if leaf := folderDescendants(folders, 4); len(leaf) != 0 {
		t.Errorf("descendants(leaf 4) = %v, want none", leaf)
	}
}

// TestAFolderShareStaysInsideTheOrganization: a folder grant opens the mailbox to
// its grantee, and the mailbox surfaces keep that inside the organization, so a
// share with an account of another organization or with no account is refused
// with 400 and leaves no permission row; a colleague is granted.
func TestAFolderShareStaysInsideTheOrganization(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}
	for _, a := range []string{"bob@hermex.test", "mallory@other-org.test"} {
		accounts[a] = directory.Account{Password: "pw", MailboxPath: t.TempDir()}
	}
	do := loginAs(t, accounts, "alice@hermex.test")
	path := "/api/v1/mailboxes/alice@hermex.test/calendar/acl"
	for _, grantee := range []string{"mallory@other-org.test", "ghost@nowhere.test"} {
		wantStatus(t, "share with "+grantee, do(http.MethodPost, path, `{"grantee":"`+grantee+`","rights":1}`), http.StatusBadRequest)
	}
	wantStatus(t, "share with a colleague", do(http.MethodPost, path, `{"grantee":"bob@hermex.test","rights":1}`), http.StatusOK)

	st, err = objectstore.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	entries, err := st.ListPermissions(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, "mallory@other-org.test") || strings.EqualFold(e.Name, "ghost@nowhere.test") {
			t.Errorf("a refused share left a permission row for %s", e.Name)
		}
	}
}

// TestAnotherAccountCannotShareYourFolder: only the owner (or a folder owner per
// [MS-OXCPERM] frightsOwner) may change a folder's permissions, so a colleague
// with no grant who posts to alice's calendar ACL is refused, and no row for
// him or for the account he names appears.
func TestAnotherAccountCannotShareYourFolder(t *testing.T) {
	dir := freshMailbox(t)
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: dir},
		"bob@hermex.test":   {Password: "pw", MailboxPath: freshMailbox(t)},
		"carol@hermex.test": {Password: "pw", MailboxPath: freshMailbox(t)},
	}
	bob := loginAs(t, accounts, "bob@hermex.test")
	rec := bob(http.MethodPost, "/api/v1/mailboxes/alice@hermex.test/calendar/acl", `{"grantee":"carol@hermex.test","rights":1}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("bob changed alice's calendar ACL: %s", rec.Body.String())
	}

	st, err := objectstore.OpenExisting(dir)
	mustNoErr(t, "open alice's mailbox", err)
	defer mustClose(t, st)
	entries, err := st.ListPermissions(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, "list permissions", err)
	for _, e := range entries {
		if strings.EqualFold(e.Name, "carol@hermex.test") || strings.EqualFold(e.Name, "bob@hermex.test") {
			t.Errorf("a refused share left a permission row for %s", e.Name)
		}
	}
}

// TestACLResolvesCalendars proves the share dialog of the calendar page reaches
// a folder: it names the default calendar "calendar" and a created calendar by
// its folder id. A folder id that is not a calendar stays unresolved.
func TestACLResolvesCalendars(t *testing.T) {
	do, dir := apiHarness(t)
	created := okBody[calendarJSON](t, "create calendar", do(http.MethodPost, "/api/v1/calendar/calendars", `{"name":"Team"}`))
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	mailFolder, err := st.CreateFolder(nil, "Projects")
	mustClose(t, st)
	mustNoErr(t, "create mail folder", err)

	for _, name := range []string{"calendar", created.ID} {
		got := okBody[map[string]any](t, "acl of "+name, do(http.MethodGet, "/api/v1/mailboxes/alice@hermex.test/"+name+"/acl", ""))
		wantEq(t, "mailbox", got["mailbox"], any(name))
	}
	wantStatus(t, "acl of a mail folder id", do(http.MethodGet, "/api/v1/mailboxes/alice@hermex.test/"+strconv.FormatInt(mailFolder, 10)+"/acl", ""), http.StatusNotFound)
}
