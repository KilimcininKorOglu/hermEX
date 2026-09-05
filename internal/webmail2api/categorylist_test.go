package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcfg"
)

// categoryHarness returns a request helper plus the mailbox path.
func categoryHarness(t *testing.T) (func(method, target, body string) *httptest.ResponseRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	secret := []byte("category-list-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	return func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}, dir
}

func readCategories(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder) []categoryJSON {
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
	defer st.Close()
	id, ok, err := st.FindAssociatedByClass(int64(mapi.PrivateFIDCalendar), oxcfg.CategoryListClass)
	if err != nil || !ok {
		t.Fatalf("no configuration message in the Calendar folder (ok=%v err=%v)", ok, err)
	}
	props, err := st.GetMessageProperties(id, mapi.PrRoamingXmlStream)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := props.Get(mapi.PrRoamingXmlStream)
	b, _ := raw.([]byte)
	l, err := oxcfg.Decode(b)
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
	defer st.Close()
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
	defer st.Close()
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

// TestCategoryListSeedsFromTheOlderList keeps a user who already had categories
// from finding an empty list after the storage moved.
func TestCategoryListSeedsFromTheOlderList(t *testing.T) {
	do, dir := categoryHarness(t)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := sharedSettings(st)
	m["categories"] = json.RawMessage(`[{"name":"Old","color":"#e7a1a2"}]`)
	if err := saveSharedSettings(st, m); err != nil {
		t.Fatal(err)
	}
	st.Close()

	cats := readCategories(t, do)
	if len(cats) != 1 || cats[0].Name != "Old" {
		t.Fatalf("categories = %+v, want the pre-existing one", cats)
	}

	// And the read seeded the interoperable copy, so Outlook sees it too.
	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok, err := st.FindAssociatedByClass(int64(mapi.PrivateFIDCalendar), oxcfg.CategoryListClass); err != nil || !ok {
		t.Errorf("the older list was not seeded into the configuration message (ok=%v err=%v)", ok, err)
	}
}
