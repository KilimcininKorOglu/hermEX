package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// favorites reads the pinned folder names the sidebar renders.
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

// pin makes a folder and pins it.
func pin(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder, name string) {
	t.Helper()
	if rec := do(http.MethodPost, "/api/v1/folders", `{"name":"`+name+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("create %s = %d", name, rec.Code)
	}
	if rec := do(http.MethodPost, "/api/v1/favorites/toggle", `{"name":"`+name+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("pin %s = %d: %s", name, rec.Code, rec.Body.String())
	}
}

// TestRenamingAFolderCarriesItsFavorite pins the fix: a favourite is stored by
// display name, so a rename that does not carry it silently unpins the folder.
func TestRenamingAFolderCarriesItsFavorite(t *testing.T) {
	do, _ := folderHarness(t)
	pin(t, do, "Project X")
	pin(t, do, "Other")

	if rec := do(http.MethodPut, "/api/v1/folders/Project%20X", `{"name":"Project Y"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	got := favorites(t, do)
	if len(got) != 2 || got[0] != "Project Y" || got[1] != "Other" {
		t.Errorf("favorites after rename = %v, want [Project Y Other]", got)
	}
}

// TestDeletingAFolderDropsItsFavorite keeps the sidebar from pointing at a
// folder that is no longer there, and leaves every other pin alone. Removing
// more than the one folder's pin is the defect this guards.
func TestDeletingAFolderDropsItsFavorite(t *testing.T) {
	do, _ := folderHarness(t)
	pin(t, do, "Project X")
	pin(t, do, "Other")

	if rec := do(http.MethodDelete, "/api/v1/folders/Project%20X", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	got := favorites(t, do)
	if len(got) != 1 || got[0] != "Other" {
		t.Errorf("favorites after the first delete = %v, want [Other]", got)
	}
}

// TestRenamingABuiltinFolderIsRefused closes the same hole the delete path had:
// the rename resolved by display name too, and the store's own guard is the
// backstop rather than the answer the caller should see.
func TestRenamingABuiltinFolderIsRefused(t *testing.T) {
	do, _ := folderHarness(t)
	rec := do(http.MethodPut, "/api/v1/folders/Inbox", `{"name":"Posteingang"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rename Inbox = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}
