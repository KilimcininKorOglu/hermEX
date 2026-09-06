package webmail2api

import (
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

	readAppearance := func(what string) appearanceSettingsJSON {
		t.Helper()
		return okBody[appearanceSettingsJSON](t, what, do(http.MethodGet, "/api/v1/settings/appearance", ""))
	}
	putAppearance := func(what, body string) {
		t.Helper()
		wantStatus(t, what, do(http.MethodPut, "/api/v1/settings/appearance", body), http.StatusOK)
	}

	// Default GET yields the Exchange defaults.
	def := readAppearance("default get")
	wantEq(t, "default theme", def.Theme, "system")
	wantEq(t, "default dateFormat", def.DateFormat, "iso")
	wantEq(t, "default timeFormat", def.TimeFormat, "24")
	wantEq(t, "default shortcutMode", def.ShortcutMode, "extended")
	wantEq(t, "default iconSet", def.IconSet, "breeze")
	wantEq(t, "default filePreview", def.FilePreview, true)
	wantEq(t, "default pdfZoom", def.PdfZoom, "page-width")

	// Put dark theme + Turkish + DMY dates + 12h, plus the unread border toggle,
	// a basic shortcut mode, and the classic icon set.
	putAppearance("put", `{"theme":"dark","language":"tr","dateFormat":"dmy","timeFormat":"12","nameDisplay":"lastfirst","showUnreadCounter":true,"unreadBorder":true,"hideWidgetPanel":false,"shortcutMode":"basic","iconSet":"classic","showItemData":true,"filePreview":false,"pdfZoom":"auto"}`)

	got := readAppearance("get")
	wantEq(t, "theme", got.Theme, "dark")
	wantEq(t, "language", got.Language, "tr")
	wantEq(t, "dateFormat", got.DateFormat, "dmy")
	wantEq(t, "timeFormat", got.TimeFormat, "12")
	wantEq(t, "nameDisplay", got.NameDisplay, "lastfirst")
	wantEq(t, "unreadBorder", got.UnreadBorder, true)
	wantEq(t, "shortcutMode", got.ShortcutMode, "basic")
	wantEq(t, "iconSet", got.IconSet, "classic")
	wantEq(t, "showItemData", got.ShowItemData, true)
	wantEq(t, "filePreview", got.FilePreview, false)
	wantEq(t, "pdfZoom", got.PdfZoom, "auto")

	// A bad enum clamps: theme "neon" → system, dateFormat "us" → iso,
	// shortcutMode "vim" → extended, iconSet "flat" → breeze.
	putAppearance("clamp put", `{"theme":"neon","dateFormat":"us","shortcutMode":"vim","iconSet":"flat","pdfZoom":"200%"}`)
	clamped := readAppearance("clamped get")
	wantEq(t, "clamped theme", clamped.Theme, "system")
	wantEq(t, "clamped dateFormat", clamped.DateFormat, "iso")
	wantEq(t, "clamped shortcutMode", clamped.ShortcutMode, "extended")
	wantEq(t, "clamped iconSet", clamped.IconSet, "breeze")
	wantEq(t, "clamped pdfZoom", clamped.PdfZoom, "page-width")
}
