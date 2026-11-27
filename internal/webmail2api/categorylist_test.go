package webmail2api

import (
	"encoding/json"
	"net/http"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcfg"
)

// categoryHarness returns a request helper plus the mailbox path.
func categoryHarness(t *testing.T) (requestFunc, string) {
	t.Helper()
	return apiHarness(t)
}

// readCategories reads the list the webmail settings page shows.
func readCategories(t *testing.T, do requestFunc) []categoryJSON {
	t.Helper()
	rec := do(http.MethodGet, "/api/v1/categories", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get categories: status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Categories []categoryJSON `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Categories
}

// storedList reads the configuration message straight out of the mailbox, the way
// Outlook would, without going through the API that wrote it.
func storedList(t *testing.T, dir string) oxcfg.List {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	id, ok, err := st.FindAssociatedByClass(int64(mapi.PrivateFIDCalendar), oxcfg.CategoryListClass)
	if err != nil || !ok {
		t.Fatalf("no configuration message in the Calendar folder (ok=%v err=%v)", ok, err)
	}
	props, err := st.GetMessageProperties(id, mapi.PrRoamingXmlStream)
	if err != nil {
		t.Fatal(err)
	}
	l, err := oxcfg.Decode(mustProp[[]byte](t, "the roaming XML stream", props, mapi.PrRoamingXmlStream))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// seedOutlookList writes a list into the mailbox the way Outlook would have.
func seedOutlookList(t *testing.T, dir string, l oxcfg.List) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	if err := writeCategoryList(st, l); err != nil {
		t.Fatal(err)
	}
}

// assertCalendarHasNoVisibleObjects proves the configuration message is
// associated. An unassociated one shows up as an item in the user's calendar.
func assertCalendarHasNoVisibleObjects(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Errorf("the calendar lists %d visible objects; the configuration message is not associated", len(objs))
	}
}

// TestCategoryListIsStoredWhereOutlookReadsIt is the interop guarantee. A category
// saved from webmail has to land in the configuration message Outlook reads, or
// the two clients show different lists for one mailbox.
func TestCategoryListIsStoredWhereOutlookReadsIt(t *testing.T) {
	do, dir := categoryHarness(t)

	if rec := do(http.MethodPut, "/api/v1/categories",
		`{"categories":[{"name":"Project X","color":"#b6cbe4"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", rec.Code, rec.Body.String())
	}

	l := storedList(t, dir)
	if len(l.Categories) != 1 || l.Categories[0].Name != "Project X" {
		t.Fatalf("stored list = %+v", l.Categories)
	}
	if l.Categories[0].Color != 7 {
		t.Errorf("colour = %d, want the palette index 7 for #b6cbe4", l.Categories[0].Color)
	}
	assertCalendarHasNoVisibleObjects(t, dir)
}

// TestCategoryEditKeepsOutlooksBookkeeping proves an edit from webmail folds onto
// the stored list. Rebuilding it would hand Outlook categories it has never seen
// and cost the user every keyboard shortcut and usage count on the first save.
func TestCategoryEditKeepsOutlooksBookkeeping(t *testing.T) {
	do, dir := categoryHarness(t)

	seedOutlookList(t, dir, oxcfg.List{Default: "Project X", Categories: []oxcfg.Category{{
		Name: "Project X", Color: 7, GUID: "{abc}", KeyboardShortcut: "3", UsageCount: "9",
	}}})

	if rec := do(http.MethodPut, "/api/v1/categories",
		`{"categories":[{"name":"Project X","color":"#c0e2b1"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("put: status %d", rec.Code)
	}

	l := storedList(t, dir)
	if len(l.Categories) != 1 {
		t.Fatalf("got %d categories", len(l.Categories))
	}
	c := l.Categories[0]
	if c.GUID != "{abc}" || c.KeyboardShortcut != "3" || c.UsageCount != "9" {
		t.Errorf("bookkeeping lost on edit: %+v", c)
	}
	if c.Color != 4 {
		t.Errorf("colour = %d, want the palette index 4 for #c0e2b1", c.Color)
	}
	if l.Default != "Project X" {
		t.Errorf("document attribute lost: default = %q", l.Default)
	}
}

// TestCategoryEditKeepsOneEntryPerName proves an edit naming one category twice,
// or in two cases, stores it once and keeps the stored entry's GUID. Outlook keys
// a category by name case-insensitively; two entries would carry the same GUID.
func TestCategoryEditKeepsOneEntryPerName(t *testing.T) {
	do, dir := categoryHarness(t)
	seedOutlookList(t, dir, oxcfg.List{Categories: []oxcfg.Category{{Name: "Project X", Color: 7, GUID: "{abc}"}}})

	if rec := do(http.MethodPut, "/api/v1/categories",
		`{"categories":[{"name":"project x","color":"#c0e2b1"},{"name":"Project X","color":"#b6cbe4"},{"name":"New","color":"#b6cbe4"},{"name":"NEW","color":"#b6cbe4"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("put: status %d", rec.Code)
	}
	l := storedList(t, dir)
	if len(l.Categories) != 2 {
		t.Fatalf("stored %d categories, want 2: %+v", len(l.Categories), l.Categories)
	}
	if c := l.Categories[0]; c.GUID != "{abc}" || c.Name != "project x" || c.Color != 4 {
		t.Errorf("first entry = %+v, want the stored GUID under the edited name and colour", c)
	}
	if l.Categories[1].Name != "New" {
		t.Errorf("second entry = %q, want New", l.Categories[1].Name)
	}
}

// TestCategoryListSeedsFromTheOlderList keeps a user who already had categories
// from finding an empty list after the storage moved.
func TestCategoryListSeedsFromTheOlderList(t *testing.T) {
	do, dir := categoryHarness(t)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := sharedSettings(st)
	if !ok {
		t.Fatal("read the shared settings")
	}
	m["categories"] = json.RawMessage(`[{"name":"Old","color":"#e7a1a2"}]`)
	if err := saveSharedSettings(st, m); err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	cats := readCategories(t, do)
	if len(cats) != 1 || cats[0].Name != "Old" {
		t.Fatalf("categories = %+v, want the pre-existing one", cats)
	}

	// And the read seeded the interoperable copy, so Outlook sees it too.
	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	if id, ok, err := st.FindAssociatedByClass(int64(mapi.PrivateFIDCalendar), oxcfg.CategoryListClass); err != nil || !ok || id <= 0 {
		t.Errorf("the older list was not seeded into the configuration message (id=%d ok=%v err=%v)", id, ok, err)
	}
}

// TestARefusedCategoryEditKeepsOutlooksList: a body that is not the list's JSON
// is refused with 400 and the configuration message Outlook reads stays as it was.
func TestARefusedCategoryEditKeepsOutlooksList(t *testing.T) {
	do, dir := categoryHarness(t)
	seedOutlookList(t, dir, oxcfg.List{Categories: []oxcfg.Category{{Name: "Project X", Color: 7, GUID: "{abc}"}}})

	for _, body := range []string{`{"categories":`, `{"categories":"Project Y"}`, `not json`} {
		wantStatus(t, "put "+body, do(http.MethodPut, "/api/v1/categories", body), http.StatusBadRequest)
	}
	l := storedList(t, dir)
	if len(l.Categories) != 1 || l.Categories[0].Name != "Project X" || l.Categories[0].GUID != "{abc}" {
		t.Errorf("stored list after refused edits = %+v, want Outlook's Project X untouched", l.Categories)
	}
}
