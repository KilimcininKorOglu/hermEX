package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// favorites reads the pinned folder slugs the sidebar renders.
func favorites(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder) []string {
	t.Helper()
	rec := do(http.MethodGet, "/api/v1/favorites", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get favorites = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Favorites []string `json:"favorites"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Favorites
}

// pin makes a folder, pins it by the slug the create answered, and returns it.
func pin(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder, name string) string {
	t.Helper()
	rec := do(http.MethodPost, "/api/v1/folders", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create %s = %d", name, rec.Code)
	}
	var created struct {
		ID string `json:"id"`
	}
	mustNoErr(t, "decode the create", json.Unmarshal(rec.Body.Bytes(), &created))
	if rec := do(http.MethodPost, "/api/v1/favorites/toggle", `{"id":"`+created.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("pin %s = %d: %s", name, rec.Code, rec.Body.String())
	}
	return created.ID
}

// TestRenamingAFolderKeepsItsFavorite proves a pin survives a rename untouched:
// the pin is the folder's id, which a rename does not change.
func TestRenamingAFolderKeepsItsFavorite(t *testing.T) {
	do, _ := folderHarness(t)
	x := pin(t, do, "Project X")
	other := pin(t, do, "Other")

	if rec := do(http.MethodPut, "/api/v1/folders/"+x, `{"name":"Project Y"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	got := favorites(t, do)
	if len(got) != 2 || got[0] != x || got[1] != other {
		t.Errorf("favorites after rename = %v, want [%s %s]", got, x, other)
	}
}

// TestDeletingAFolderDropsItsFavorite keeps the sidebar from pointing at a
// folder that is no longer there, and leaves every other pin alone. Removing
// more than the one folder's pin is the defect this guards.
func TestDeletingAFolderDropsItsFavorite(t *testing.T) {
	do, _ := folderHarness(t)
	x := pin(t, do, "Project X")
	other := pin(t, do, "Other")

	if rec := do(http.MethodDelete, "/api/v1/folders/"+x, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	got := favorites(t, do)
	if len(got) != 1 || got[0] != other {
		t.Errorf("favorites after the first delete = %v, want [%s]", got, other)
	}
}

// TestDeletingAFolderKeepsItsNameInTheTrash proves a folder deleted by its slug
// lands under Deleted Items with its own display name, not the slug.
func TestDeletingAFolderKeepsItsNameInTheTrash(t *testing.T) {
	do, dir := folderHarness(t)
	x := pin(t, do, "Project X")
	if rec := do(http.MethodDelete, "/api/v1/folders/"+x, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	fid, _ := parseCustomSlug(x)
	props, err := st.GetFolderProperties(fid, mapi.PrDisplayName)
	mustNoErr(t, "read the name", err)
	wantEq(t, "the name under Deleted Items", propStr(props, mapi.PrDisplayName), "Project X")
}

// TestLegacyFavoritesBecomeIDs proves pins stored by display name before folders
// had ids are read once into the slugs of the folders they reached, a name that
// reaches no folder is dropped, the next read answers the same, and every other
// settings key is left as it was.
func TestLegacyFavoritesBecomeIDs(t *testing.T) {
	do, dir := folderHarness(t)
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	fid, err := st.CreateFolder(nil, "Project X")
	mustNoErr(t, "create Project X", err)
	m := map[string]json.RawMessage{
		legacyFavoritesKey:  json.RawMessage(`["Project X","inbox","Gone"]`),
		"webmail2Unrelated": json.RawMessage(`{"keep":true}`),
	}
	mustNoErr(t, "write the legacy blob", saveSharedSettings(st, m))
	st.Close()

	want := []string{customSlug(fid), "inbox"}
	for _, pass := range []string{"first", "second"} {
		got := favorites(t, do)
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s read = %v, want %v", pass, got, want)
		}
	}
	st, err = objectstore.Open(dir)
	mustNoErr(t, "reopen the store", err)
	defer st.Close()
	after, loaded := sharedSettings(st)
	wantEq(t, "settings readable", loaded, true)
	wantEq(t, "the unrelated key", string(after["webmail2Unrelated"]), `{"keep":true}`)
	if _, still := after[legacyFavoritesKey]; still {
		t.Error("the legacy favourites key is still stored")
	}
}

// TestRenamingABuiltinFolderIsRefused closes the same hole the delete path had:
// the rename resolved by display name too. The display name, the slug and the
// Inbox's id slug must all be refused, and the Inbox keeps its name.
func TestRenamingABuiltinFolderIsRefused(t *testing.T) {
	do, dir := folderHarness(t)
	for _, ref := range []string{"Inbox", "inbox", "f13"} {
		if rec := do(http.MethodPut, "/api/v1/folders/"+ref, `{"name":"Posteingang"}`); rec.Code == http.StatusOK {
			t.Errorf("rename %s = 200, want a refusal", ref)
		}
	}
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	props, err := st.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrDisplayName)
	mustNoErr(t, "read the Inbox name", err)
	wantEq(t, "the Inbox name", propStr(props, mapi.PrDisplayName), "Inbox")
}
