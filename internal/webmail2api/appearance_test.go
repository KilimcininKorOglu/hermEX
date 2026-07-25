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

// TestAppearanceSettingsRoundTrip proves the display settings persist across a
// put-then-get, and that a bad enum clamps to a valid default rather than being
// stored verbatim (so a corrupt client cannot wedge the theme).
func TestAppearanceSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	secret := []byte("appearance-test-secret")
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

	// Default GET yields the Exchange defaults.
	rec := do(http.MethodGet, "/api/v1/settings/appearance", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("default get: status %d", rec.Code)
	}
	var def appearanceSettingsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatalf("decode default: %v", err)
	}
	if def.Theme != "system" || def.DateFormat != "iso" || def.TimeFormat != "24" {
		t.Errorf("defaults = %+v, want system/iso/24", def)
	}
	if def.ShortcutMode != "extended" {
		t.Errorf("default shortcutMode = %q, want extended", def.ShortcutMode)
	}

	// Put dark theme + Turkish + DMY dates + 12h, plus the unread border toggle
	// and a basic shortcut mode.
	body := `{"theme":"dark","language":"tr","dateFormat":"dmy","timeFormat":"12","nameDisplay":"lastfirst","showUnreadCounter":true,"unreadBorder":true,"hideWidgetPanel":false,"shortcutMode":"basic"}`
	if rec := do(http.MethodPut, "/api/v1/settings/appearance", body); rec.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", rec.Code, rec.Body.String())
	}

	rec = do(http.MethodGet, "/api/v1/settings/appearance", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d", rec.Code)
	}
	var got appearanceSettingsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Theme != "dark" || got.Language != "tr" || got.DateFormat != "dmy" || got.TimeFormat != "12" || got.NameDisplay != "lastfirst" || !got.UnreadBorder {
		t.Errorf("got = %+v, want dark/tr/dmy/12/lastfirst/UnreadBorder", got)
	}
	if got.ShortcutMode != "basic" {
		t.Errorf("got shortcutMode = %q, want basic", got.ShortcutMode)
	}

	// A bad enum clamps: theme "neon" → system, dateFormat "us" → iso,
	// shortcutMode "vim" → extended.
	if rec := do(http.MethodPut, "/api/v1/settings/appearance", `{"theme":"neon","dateFormat":"us","shortcutMode":"vim"}`); rec.Code != http.StatusOK {
		t.Fatalf("clamp put: status %d", rec.Code)
	}
	rec = do(http.MethodGet, "/api/v1/settings/appearance", "")
	var clamped appearanceSettingsJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &clamped)
	if clamped.Theme != "system" || clamped.DateFormat != "iso" || clamped.ShortcutMode != "extended" {
		t.Errorf("clamped = %+v, want system/iso/extended", clamped)
	}
}
