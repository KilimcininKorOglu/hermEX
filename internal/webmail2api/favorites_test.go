package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFavoritesTogglePersists proves the favourite-folder toggle pins and unpins a
// folder, by its slug, in the user's webmail settings and survives a re-read, and
// that a slug naming no folder is refused rather than pinned.
func TestFavoritesTogglePersists(t *testing.T) {
	dir := freshMailbox(t)
	fid, err := openMailbox(t, dir).CreateFolder(nil, "Project")
	mustNoErr(t, "create Project", err)
	slug := customSlug(fid)
	do := apiHarnessFor(t, dir)
	favs := func(rec *httptest.ResponseRecorder) []string {
		var out struct {
			Favorites []string `json:"favorites"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode favorites: %v (body %s)", err, rec.Body.String())
		}
		return out.Favorites
	}

	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 0 {
		t.Fatalf("initial favorites = %v, want empty", got)
	}
	if rec := do(http.MethodPost, "/api/v1/favorites/toggle", `{"id":"`+slug+`"}`); rec.Code != 200 {
		t.Fatalf("toggle on: %d %s", rec.Code, rec.Body.String())
	}
	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 1 || got[0] != slug {
		t.Errorf("after pin, favorites = %v, want [%s]", got, slug)
	}
	// A refused toggle leaves the existing pin alone.
	if rec := do(http.MethodPost, "/api/v1/favorites/toggle", `{"id":"f999999"}`); rec.Code == http.StatusOK {
		t.Errorf("toggle of a missing folder answered 200")
	}
	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 1 || got[0] != slug {
		t.Errorf("after a refused toggle, favorites = %v, want [%s]", got, slug)
	}
	wantStatus(t, "toggle off", do(http.MethodPost, "/api/v1/favorites/toggle", `{"id":"`+slug+`"}`), http.StatusOK)
	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 0 {
		t.Errorf("after unpin, favorites = %v, want empty", got)
	}
}

// TestFavoritesToggleRefusesAnUnknownFolder proves a toggle naming no folder (a
// missing id, a malformed slug, no id at all) is refused and pins nothing, so a
// pin always names a real folder.
func TestFavoritesToggleRefusesAnUnknownFolder(t *testing.T) {
	do := folderDo(t)
	for _, body := range []string{`{"id":"f999999"}`, `{"id":"f13x"}`, `{}`} {
		if rec := do(http.MethodPost, "/api/v1/favorites/toggle", body); rec.Code == http.StatusOK {
			t.Errorf("toggle %s = 200, want a refusal", body)
		}
	}
	if got := favorites(t, do); len(got) != 0 {
		t.Errorf("after refused toggles, favorites = %v, want empty", got)
	}
}
