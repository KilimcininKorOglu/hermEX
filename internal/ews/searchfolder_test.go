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

// createSearchFolderReq is a CreateFolder of one search folder under
// searchfolders with the given SearchParameters.
func createSearchFolderReq(params string) string {
	return wrapRequest(`<CreateFolder xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<ParentFolderId><t:DistinguishedFolderId Id="searchfolders"/></ParentFolderId>` +
		`<Folders><t:SearchFolder><t:DisplayName>Hellos</t:DisplayName>` + params + `</t:SearchFolder></Folders>` +
		`</CreateFolder>`)
}

// TestCreateFolderMakesASearchFolder proves a t:SearchFolder a client creates
// is a store search folder over its SearchParameters: FindItem on it lists the
// mail its restriction finds in its base folders.
func TestCreateFolderMakesASearchFolder(t *testing.T) {
	_, post := searchFolderMailbox(t)
	out := post(createSearchFolderReq(`<t:SearchParameters Traversal="Shallow"><t:Restriction>` +
		`<t:Contains ContainmentMode="Substring" ContainmentComparison="IgnoreCase"><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="unrelated"/></t:Contains>` +
		`</t:Restriction><t:BaseFolderIds><t:DistinguishedFolderId Id="inbox"/></t:BaseFolderIds></t:SearchParameters>`))
	id := searchFolderIDRE.FindStringSubmatch(out)
	if len(id) != 2 {
		t.Fatalf("CreateFolder did not answer a SearchFolder: %s", out)
	}
	items := post(wrapRequest(`<FindItem Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>Default</BaseShape></ItemShape>` +
		`<ParentFolderIds><t:FolderId Id="` + id[1] + `" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindItem>`))
	if !strings.Contains(items, "Unrelated") || strings.Contains(items, "Hello EWS") {
		t.Errorf("the created search folder lists %s, want the Unrelated message only", items)
	}
}

// TestCreateFolderRefusesASearchFolderItCannotRun proves a search folder whose
// parameters cannot be applied is refused and not left behind empty.
func TestCreateFolderRefusesASearchFolderItCannotRun(t *testing.T) {
	dir, post := searchFolderMailbox(t)
	cases := []struct{ name, params, code string }{
		{"no restriction",
			`<t:SearchParameters><t:BaseFolderIds><t:DistinguishedFolderId Id="inbox"/></t:BaseFolderIds></t:SearchParameters>`,
			"ErrorInvalidRestriction"},
		{"no base folders",
			`<t:SearchParameters><t:Restriction><t:Exists><t:FieldURI FieldURI="item:Subject"/></t:Exists></t:Restriction></t:SearchParameters>`,
			"ErrorInvalidRequest"},
		{"an unknown traversal",
			`<t:SearchParameters Traversal="Sideways"><t:Restriction><t:Exists><t:FieldURI FieldURI="item:Subject"/></t:Exists></t:Restriction>` +
				`<t:BaseFolderIds><t:DistinguishedFolderId Id="inbox"/></t:BaseFolderIds></t:SearchParameters>`,
			"ErrorInvalidRequest"},
		{"its own parent as a base folder",
			`<t:SearchParameters><t:Restriction><t:Exists><t:FieldURI FieldURI="item:Subject"/></t:Exists></t:Restriction>` +
				`<t:BaseFolderIds><t:DistinguishedFolderId Id="searchfolders"/></t:BaseFolderIds></t:SearchParameters>`,
			"ErrorInvalidOperation"},
	}
	for _, c := range cases {
		if out := post(createSearchFolderReq(c.params)); !strings.Contains(out, "<ResponseCode>"+c.code+"</ResponseCode>") {
			t.Errorf("%s: CreateFolder = %s, want %s", c.name, out, c.code)
		}
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	finder, err := st.SearchFolderChildren(int64(mapi.PrivateFIDFinder))
	if err != nil || len(finder) != 1 {
		t.Errorf("Finder holds %+v (%v), want only the seeded search folder", finder, err)
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
