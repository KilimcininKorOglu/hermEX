package ews

import (
	"regexp"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

var searchFolderIDRE = regexp.MustCompile(`<SearchFolder xmlns="[^"]+"><FolderId Id="([^"]+)"`)

// searchFolderMailbox seeds two messages in the Inbox and a search folder under
// Finder that finds the one whose subject names "Hello".
func searchFolderMailbox(t *testing.T) (string, func(body string) string) {
	t.Helper()
	other := strings.Replace(plainMessage, "Subject: Hello EWS", "Subject: Unrelated", 1)
	ts, dir := seededWithMessage(t, plainMessage, other)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fid, err := st.CreateSearchFolder(int64(mapi.PrivateFIDFinder), "Greetings")
	if err != nil {
		t.Fatal(err)
	}
	hello := mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		FuzzyLevel: 0x00000001 | 0x00010000, PropTag: mapi.PrSubject,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrSubject, Value: "hello"},
	}}
	if err := st.SetSearchCriteria(fid, objectstore.SearchCriteria{
		Restriction: &hello, Scope: []int64{int64(mapi.PrivateFIDInbox)}, Flags: mapi.SearchRestart,
	}); err != nil {
		t.Fatal(err)
	}
	return dir, func(body string) string {
		t.Helper()
		resp, out := soapPost(t, ts, body, true)
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
		return out
	}
}

// TestFindFolderListsTheSearchFolders proves the searchfolders folder lists the
// search folders under Finder, each as a t:SearchFolder that counts the mail it
// finds. A client tells a search folder from a folder by the element name.
func TestFindFolderListsTheSearchFolders(t *testing.T) {
	_, post := searchFolderMailbox(t)
	out := post(wrapRequest(`<FindFolder Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="searchfolders" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindFolder>`))
	if !searchFolderIDRE.MatchString(out) || !strings.Contains(out, "<DisplayName>Greetings</DisplayName>") {
		t.Fatalf("FindFolder did not list the search folder as a SearchFolder: %s", out)
	}
	if !strings.Contains(out, "<TotalCount>1</TotalCount>") || !strings.Contains(out, "<UnreadCount>1</UnreadCount>") {
		t.Errorf("the search folder does not count the one message it finds: %s", out)
	}
}

// TestFindItemListsWhatASearchFolderFinds proves FindItem on a search folder
// lists the mail it finds, and that a result's id opens the message itself.
func TestFindItemListsWhatASearchFolderFinds(t *testing.T) {
	_, post := searchFolderMailbox(t)
	folders := post(wrapRequest(`<FindFolder Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="searchfolders" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindFolder>`))
	id := searchFolderIDRE.FindStringSubmatch(folders)
	if len(id) != 2 {
		t.Fatalf("no search folder id: %s", folders)
	}
	out := post(wrapRequest(`<FindItem Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>Default</BaseShape></ItemShape>` +
		`<ParentFolderIds><t:FolderId Id="` + id[1] + `" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindItem>`))
	if !strings.Contains(out, "Hello EWS") || strings.Contains(out, "Unrelated") {
		t.Fatalf("FindItem on the search folder = %s, want the Hello message only", out)
	}
	item := itemIDRE.FindStringSubmatch(out)
	if len(item) != 2 {
		t.Fatalf("no item id: %s", out)
	}
	if got := post(getItemReq(item[1])); !strings.Contains(got, "the body text") {
		t.Errorf("GetItem on a search result = %s, want the message", got)
	}
}
