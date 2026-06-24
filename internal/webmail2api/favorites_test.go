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
// folder in the user's webmail settings and survives a re-read — the old webmail's
// favourites, persisted in the settings blob.
func TestFavoritesTogglePersists(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("favorites-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
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
	}
	favs := func(rec *httptest.ResponseRecorder) []string {
		var out struct {
			Favorites []string `json:"favorites"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Favorites
	}

	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 0 {
		t.Fatalf("initial favorites = %v, want empty", got)
	}
	if rec := do(http.MethodPost, "/api/v1/favorites/toggle", `{"name":"Project"}`); rec.Code != 200 {
		t.Fatalf("toggle on: %d %s", rec.Code, rec.Body.String())
	}
	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 1 || got[0] != "Project" {
		t.Errorf("after pin, favorites = %v, want [Project]", got)
	}
	do(http.MethodPost, "/api/v1/favorites/toggle", `{"name":"Project"}`)
	if got := favs(do(http.MethodGet, "/api/v1/favorites", "")); len(got) != 0 {
		t.Errorf("after unpin, favorites = %v, want empty", got)
	}
}
