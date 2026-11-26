package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestFavoritesTogglePersists proves the favourite-folder toggle pins and unpins a
// folder, by its slug, in the user's webmail settings and survives a re-read, and
// that a slug naming no folder is refused rather than pinned.
func TestFavoritesTogglePersists(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	fid, err := st.CreateFolder(nil, "Project")
	mustNoErr(t, "create Project", err)
	st.Close()
	slug := customSlug(fid)

	secret := []byte("favorites-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
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
	}
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
	do(http.MethodPost, "/api/v1/favorites/toggle", `{"id":"`+slug+`"}`)
	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 0 {
		t.Errorf("after unpin, favorites = %v, want empty", got)
	}
}

// TestFavoritesToggleRefusesAnUnknownFolder proves a toggle naming no folder (a
// missing id, a malformed slug, no id at all) is refused and pins nothing, so a
// pin always names a real folder.
func TestFavoritesToggleRefusesAnUnknownFolder(t *testing.T) {
	do, _ := folderHarness(t)
	for _, body := range []string{`{"id":"f999999"}`, `{"id":"f13x"}`, `{}`} {
		if rec := do(http.MethodPost, "/api/v1/favorites/toggle", body); rec.Code == http.StatusOK {
			t.Errorf("toggle %s = 200, want a refusal", body)
		}
	}
	if got := favorites(t, do); len(got) != 0 {
		t.Errorf("after refused toggles, favorites = %v, want empty", got)
	}
}
