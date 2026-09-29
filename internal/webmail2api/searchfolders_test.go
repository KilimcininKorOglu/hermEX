package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// savedSearchStore opens a fresh mailbox with a few messages in it.
func savedSearchStore(t *testing.T) (string, *objectstore.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, st
}

// appendAt files a message delivered at the given time.
func appendAt(t *testing.T, st *objectstore.Store, fid int64, from, subject, at string) {
	t.Helper()
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: " + from + "\r\nSubject: " + subject + "\r\n\r\nbody of " + subject + "\r\n"
	if _, err := st.AppendMessage(fid, []byte(raw), when, 0); err != nil {
		t.Fatal(err)
	}
}

// subjectsOf lists the subjects of a result set.
func subjectsOf(rows []mailJSON) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Subject] = true
	}
	return out
}

// TestSavedSearchDaysAreTheCallersDays proves a saved search's day range is read
// as whole days in the caller's zone: mail the caller sees on the to day is in,
// and mail it sees on the day before or after is out.
func TestSavedSearchDaysAreTheCallersDays(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	_, st := savedSearchStore(t)
	defer st.Close()
	inbox := int64(mapi.PrivateFIDInbox)
	appendAt(t, st, inbox, "a@hermex.test", "before", "2026-11-26T04:59:00Z") // 25 Nov 23:59 in New York
	appendAt(t, st, inbox, "a@hermex.test", "first", "2026-11-26T05:00:00Z")  // 26 Nov 00:00
	appendAt(t, st, inbox, "a@hermex.test", "late", "2026-11-27T02:00:00Z")   // 26 Nov 21:00
	appendAt(t, st, inbox, "a@hermex.test", "after", "2026-11-27T05:00:00Z")  // 27 Nov 00:00

	sf, err := createSavedSearch(st, searchFolderJSON{Name: "day", DateFrom: "2026-11-26", DateTo: "2026-11-26"}, ny)
	if err != nil {
		t.Fatal(err)
	}
	fid := mustParseID(t, sf.ID)
	rows, err := savedSearchMail(st, fid)
	if err != nil {
		t.Fatal(err)
	}
	got := subjectsOf(rows)
	if len(got) != 2 || !got["first"] || !got["late"] {
		t.Errorf("the 26 November search holds %v, want first and late", got)
	}
	read, err := readSearchFolder(st, fid, "day", ny)
	if err != nil {
		t.Fatal(err)
	}
	if read.DateFrom != "2026-11-26" || read.DateTo != "2026-11-26" {
		t.Errorf("the saved days read back as %s..%s", read.DateFrom, read.DateTo)
	}
}

// mustParseID parses a saved search id.
func mustParseID(t *testing.T, id string) int64 {
	t.Helper()
	var fid int64
	if err := json.Unmarshal([]byte(id), &fid); err != nil {
		t.Fatalf("saved search id %q: %v", id, err)
	}
	return fid
}

// readJSON decodes a successful answer.
func readJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

// TestSavedSearchIsAStoreSearchFolder proves a saved search made in webmail is a
// search folder in Finder, the folder a MAPI or EWS client lists, that its fields
// read back unchanged, that it holds the matching mail, and that an edit and a
// delete reach the store.
func TestSavedSearchIsAStoreSearchFolder(t *testing.T) {
	dir, st := savedSearchStore(t)
	appendAt(t, st, int64(mapi.PrivateFIDInbox), "Boss <boss@corp.test>", "Quarterly report", "2026-03-01T10:00:00Z")
	appendAt(t, st, int64(mapi.PrivateFIDInbox), "friend@else.test", "Quarterly party", "2026-03-02T10:00:00Z")
	appendAt(t, st, int64(mapi.PrivateFIDSentItems), "me@hermex.test", "Quarterly report reply", "2026-03-03T10:00:00Z")
	st.Close()
	do := loginAs(t, directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}, "alice@hermex.test")

	var made searchFolderJSON
	readJSON(t, do(http.MethodPost, "/api/v1/search-folders", `{"name":"Boss","from":"boss@corp","subject":"quarterly","base_folders":["inbox"]}`), &made)
	want := searchFolderJSON{ID: made.ID, Name: "Boss", From: "boss@corp", Subject: "quarterly", BaseFolders: []string{"inbox"}}
	if list := listSaved(t, do); len(list) != 1 || !sameSearch(list[0], want) {
		t.Fatalf("saved searches = %+v, want %+v", list, want)
	}
	if got := resultSubjects(t, do, made.ID); len(got) != 1 || !got["Quarterly report"] {
		t.Errorf("results = %v, want the boss's report only", got)
	}
	if names := finderNames(t, dir); len(names) != 1 || names[0] != "Boss" {
		t.Fatalf("Finder holds %v, want the saved search", names)
	}
	editAndDelete(t, do, made.ID)
}

// editAndDelete proves an edit changes what the saved search holds and a delete
// removes it.
func editAndDelete(t *testing.T, do requestFunc, id string) {
	t.Helper()
	var edited searchFolderJSON
	readJSON(t, do(http.MethodPut, "/api/v1/search-folders/"+id, `{"name":"Parties","subject":"party"}`), &edited)
	if got := resultSubjects(t, do, id); len(got) != 1 || !got["Quarterly party"] {
		t.Errorf("results after the edit = %v, want the party only", got)
	}
	if rec := do(http.MethodDelete, "/api/v1/search-folders/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if list := listSaved(t, do); len(list) != 0 {
		t.Errorf("saved searches after delete = %+v", list)
	}
}

// listSaved reads the caller's saved searches.
func listSaved(t *testing.T, do requestFunc) []searchFolderJSON {
	t.Helper()
	var list struct {
		SearchFolders []searchFolderJSON `json:"search_folders"`
	}
	readJSON(t, do(http.MethodGet, "/api/v1/search-folders", ""), &list)
	return list.SearchFolders
}

// resultSubjects reads the subjects a saved search holds.
func resultSubjects(t *testing.T, do requestFunc, id string) map[string]bool {
	t.Helper()
	var res struct {
		Emails []mailJSON `json:"emails"`
	}
	readJSON(t, do(http.MethodGet, "/api/v1/search-folders/"+id+"/results", ""), &res)
	return subjectsOf(res.Emails)
}

// finderNames reads the names of the search folders under Finder.
func finderNames(t *testing.T, dir string) []string {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	finder, err := st.SearchFolderChildren(int64(mapi.PrivateFIDFinder))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(finder))
	for _, f := range finder {
		names = append(names, f.DisplayName)
	}
	return names
}

// settingsBlob reads the mailbox's webmail settings as their keys.
func settingsBlob(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blob, err := st.GetWebmailSettings()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// sameSearch compares the fields a saved search round-trips.
func sameSearch(a, b searchFolderJSON) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// TestLegacySavedSearchesBecomeSearchFolders proves a saved search the settings
// blob holds becomes a search folder the first time saved searches are read, and
// leaves the blob.
func TestLegacySavedSearchesBecomeSearchFolders(t *testing.T) {
	dir, st := savedSearchStore(t)
	appendAt(t, st, int64(mapi.PrivateFIDInbox), "a@hermex.test", "invoice 7", "2026-03-01T10:00:00Z")
	if err := st.SetWebmailSettings(`{"webmail2SearchFolders":[{"id":"ab12cd34","name":"Invoices","subject":"invoice"}],"theme":"dark"}`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	do := loginAs(t, directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}, "alice@hermex.test")

	list := listSaved(t, do)
	if len(list) != 1 || list[0].Name != "Invoices" || list[0].Subject != "invoice" {
		t.Fatalf("saved searches = %+v, want the moved Invoices search", list)
	}
	if list := listSaved(t, do); len(list) != 1 {
		t.Errorf("a second read holds %d saved searches, want the one moved once", len(list))
	}
	m := settingsBlob(t, dir)
	if _, ok := m[legacySearchFoldersKey]; ok {
		t.Error("the settings blob still holds the saved searches")
	}
	if string(m["theme"]) != `"dark"` {
		t.Errorf("the move dropped another setting: %v", m)
	}
}

// TestAnotherClientsSearchFolderKeepsItsCriteria proves a search folder whose
// criteria webmail cannot express reads as custom and an edit only renames it,
// so a webmail save never widens a search Outlook defined.
func TestAnotherClientsSearchFolderKeepsItsCriteria(t *testing.T) {
	dir, st := savedSearchStore(t)
	fid, err := st.CreateSearchFolder(int64(mapi.PrivateFIDFinder), "Important")
	if err != nil {
		t.Fatal(err)
	}
	important := mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop: mapi.RelopEQ, PropTag: mapi.PrImportance,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrImportance, Value: int32(mapi.ImportanceHigh)},
	}}
	if err := st.SetSearchCriteria(fid, objectstore.SearchCriteria{Restriction: &important, Scope: []int64{int64(mapi.PrivateFIDInbox)}, Flags: mapi.SearchRestart}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	do := loginAs(t, directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}, "alice@hermex.test")
	id := jsonID(fid)

	if list := listSaved(t, do); len(list) != 1 || !list[0].Custom {
		t.Fatalf("saved searches = %+v, want one custom search", list)
	}
	var renamed searchFolderJSON
	readJSON(t, do(http.MethodPut, "/api/v1/search-folders/"+id, `{"name":"Urgent","subject":"x"}`), &renamed)

	if r := storedRestriction(t, dir, fid); r == nil || r.Type != mapi.ResProperty {
		t.Errorf("the edit replaced the other client's criteria with %+v", r)
	}
	if names := finderNames(t, dir); len(names) != 1 || names[0] != "Urgent" {
		t.Errorf("Finder holds %v, want the renamed folder", names)
	}
}

// storedRestriction reads the restriction a search folder holds in the store.
func storedRestriction(t *testing.T, dir string, fid int64) *mapi.Restriction {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _, err := st.GetSearchCriteria(fid)
	if err != nil {
		t.Fatal(err)
	}
	return c.Restriction
}

// jsonID renders a folder id as the SPA's saved search id.
func jsonID(fid int64) string {
	b, _ := json.Marshal(fid)
	return string(b)
}

// TestSavedSearchIDNamesOnlyASavedSearch proves a client id naming an ordinary
// folder reads as no saved search, so the endpoint cannot rename or delete one.
func TestSavedSearchIDNamesOnlyASavedSearch(t *testing.T) {
	dir, st := savedSearchStore(t)
	st.Close()
	do := loginAs(t, directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}, "alice@hermex.test")
	inbox := jsonID(int64(mapi.PrivateFIDInbox))
	for _, c := range []struct{ method, target, body string }{
		{http.MethodPut, "/api/v1/search-folders/" + inbox, `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/search-folders/" + inbox, ""},
		{http.MethodGet, "/api/v1/search-folders/" + inbox + "/results", ""},
	} {
		if rec := do(c.method, c.target, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.target, rec.Code)
		}
	}
}
