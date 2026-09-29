package ews

import (
	"regexp"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
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

// getSearchFolderReq is a GetFolder of one folder id in the given base shape.
func getSearchFolderReq(id, shape string) string {
	return wrapRequest(`<GetFolder xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<FolderShape><t:BaseShape>` + shape + `</t:BaseShape></FolderShape>` +
		`<FolderIds><t:FolderId Id="` + id + `"/></FolderIds></GetFolder>`)
}

var searchParamsRE = regexp.MustCompile(`<SearchParameters Traversal="[^"]+">.*</SearchParameters>`)

// TestGetFolderReturnsSearchParameters proves GetFolder in the AllProperties
// shape returns what a search folder searches for and where, and that the
// parameters it returns create a folder that finds the same mail.
func TestGetFolderReturnsSearchParameters(t *testing.T) {
	_, post := searchFolderMailbox(t)
	params := `<t:SearchParameters Traversal="Deep"><t:Restriction><t:And>` +
		`<t:Contains ContainmentMode="Prefixed" ContainmentComparison="IgnoreCase"><t:FieldURI FieldURI="item:Subject"/><t:Constant Value="unrel"/></t:Contains>` +
		`<t:IsEqualTo><t:FieldURI FieldURI="message:IsRead"/><t:FieldURIOrConstant><t:Constant Value="false"/></t:FieldURIOrConstant></t:IsEqualTo>` +
		`<t:Not><t:Exists><t:ExtendedFieldURI DistinguishedPropertySetId="PublicStrings" PropertyName="x-tag" PropertyType="String"/></t:Exists></t:Not>` +
		`<t:Excludes><t:FieldURI FieldURI="item:Sensitivity"/><t:Bitmask Value="2"/></t:Excludes>` +
		`</t:And></t:Restriction><t:BaseFolderIds><t:DistinguishedFolderId Id="inbox"/></t:BaseFolderIds></t:SearchParameters>`
	id := searchFolderIDRE.FindStringSubmatch(post(createSearchFolderReq(params)))
	if len(id) != 2 {
		t.Fatal("CreateFolder did not create the search folder")
	}
	if out := post(getSearchFolderReq(id[1], "Default")); strings.Contains(out, "SearchParameters") {
		t.Errorf("the Default shape returned the search parameters: %s", out)
	}
	out := post(getSearchFolderReq(id[1], "AllProperties"))
	got := searchParamsRE.FindString(out)
	for _, want := range []string{
		`Traversal="Deep"`,
		`<Contains ContainmentMode="Prefixed" ContainmentComparison="IgnoreCase"><FieldURI FieldURI="item:Subject"></FieldURI><Constant Value="unrel"></Constant></Contains>`,
		`<IsEqualTo><FieldURI FieldURI="message:IsRead"></FieldURI><FieldURIOrConstant><Constant Value="false"></Constant></FieldURIOrConstant></IsEqualTo>`,
		`<Not><Exists><ExtendedFieldURI DistinguishedPropertySetId="PublicStrings" PropertyName="x-tag" PropertyType="String"></ExtendedFieldURI></Exists></Not>`,
		`<Excludes><ExtendedFieldURI PropertyTag="0x36" PropertyType="Integer"></ExtendedFieldURI><Bitmask Value="2"></Bitmask></Excludes>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("SearchParameters lack %s: %s", want, out)
		}
	}
	// The parameters a client reads back make a folder that finds the same mail.
	again := strings.NewReplacer("</", "</t:", "<", "<t:").Replace(got)
	copyID := searchFolderIDRE.FindStringSubmatch(post(createSearchFolderReq(again)))
	if len(copyID) != 2 {
		t.Fatalf("the returned SearchParameters do not create a folder: %s", again)
	}
	items := post(wrapRequest(`<FindItem Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>Default</BaseShape></ItemShape>` +
		`<ParentFolderIds><t:FolderId Id="` + copyID[1] + `" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindItem>`))
	if !strings.Contains(items, "Unrelated") || strings.Contains(items, "Hello EWS") {
		t.Errorf("the copied search folder lists %s, want the Unrelated message only", items)
	}
}

// TestGetFolderLeavesOutCriteriaEWSCannotState proves a search folder whose
// criteria have no EWS form (a size test) is still served, without the
// SearchParameters that would state a different search.
func TestGetFolderLeavesOutCriteriaEWSCannotState(t *testing.T) {
	dir, post := searchFolderMailbox(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := st.CreateSearchFolder(int64(mapi.PrivateFIDFinder), "Large")
	if err == nil {
		large := mapi.Restriction{Type: mapi.ResSize, Value: mapi.SizeRestriction{Relop: mapi.RelopGT, PropTag: mapi.PrBody, Size: 1}}
		err = st.SetSearchCriteria(fid, objectstore.SearchCriteria{
			Restriction: &large, Scope: []int64{int64(mapi.PrivateFIDInbox)}, Flags: mapi.SearchRestart,
		})
	}
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	out := post(getSearchFolderReq(oxews.EncodeFolderIDFor(fid, ""), "AllProperties"))
	if !strings.Contains(out, "<DisplayName>Large</DisplayName>") || strings.Contains(out, "SearchParameters") {
		t.Errorf("GetFolder = %s, want the folder without SearchParameters", out)
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
