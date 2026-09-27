package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestCataloguesMatch proves every language carries the same keys, none of them
// empty, with the same placeholders, so a page never shows a key or loses a value
// in one language.
func TestCataloguesMatch(t *testing.T) {
	en := catalogues[defaultLang]
	if len(en) == 0 {
		t.Fatal("the English catalogue is empty")
	}
	placeholder := regexp.MustCompile(`\{\d+\}`)
	for _, lang := range directory.UILanguages {
		cat := catalogues[lang]
		for key, text := range en {
			other, ok := cat[key]
			switch {
			case !ok:
				t.Errorf("%s lacks %q", lang, key)
			case strings.TrimSpace(other) == "":
				t.Errorf("%s has an empty %q", lang, key)
			case strings.Join(placeholder.FindAllString(other, -1), ",") != strings.Join(placeholder.FindAllString(text, -1), ","):
				t.Errorf("%s %q has placeholders %v, English has %v", lang, key,
					placeholder.FindAllString(other, -1), placeholder.FindAllString(text, -1))
			}
		}
		for key := range cat {
			if _, ok := en[key]; !ok {
				t.Errorf("%s has %q, which English lacks", lang, key)
			}
		}
	}
}

// catalogueGroups returns the top-level groups of the English catalogue; a string
// whose first dotted segment names one is meant as a key.
func catalogueGroups() map[string]bool {
	groups := map[string]bool{}
	for key := range catalogues[defaultLang] {
		group, _, _ := strings.Cut(key, ".")
		groups[group] = true
	}
	return groups
}

// keyLike reports whether s reads as a catalogue key: dotted words whose first
// segment is a catalogue group. A template file name shares that shape, so a
// name ending in .html is not one.
func keyLike(groups map[string]bool, s string) bool {
	group, rest, ok := strings.Cut(s, ".")
	return ok && groups[group] && rest != "" && !strings.ContainsAny(s, " /") && !strings.HasSuffix(s, ".html")
}

// TestTemplateKeysExist proves every key a template names is in the catalogue,
// so a page never shows a raw key.
func TestTemplateKeysExist(t *testing.T) {
	groups := catalogueGroups()
	quoted := regexp.MustCompile(`"([A-Za-z0-9_.]+)"`)
	err := fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		for _, m := range quoted.FindAllStringSubmatch(string(raw), -1) {
			if keyLike(groups, m[1]) && catalogues[defaultLang][m[1]] == "" {
				t.Errorf("%s names %q, which the catalogue lacks", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGoKeysExist proves every key a handler hands a template is in the
// catalogue: a string literal in the package's Go source that reads as a key.
func TestGoKeysExist(t *testing.T) {
	groups := catalogueGroups()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for pos, key := range stringLiterals(f) {
			if keyLike(groups, key) && catalogues[defaultLang][key] == "" {
				t.Errorf("%s names %q, which the catalogue lacks", fset.Position(pos), key)
			}
		}
	}
}

// stringLiterals returns every string literal in f by its position.
func stringLiterals(f *ast.File) map[token.Pos]string {
	out := map[token.Pos]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out[lit.Pos()] = s
			}
		}
		return true
	})
	return out
}

// TestLDAPFieldsHaveLabels proves every syncable profile field has a label, since
// the binding page builds the key at render time where TestTemplateKeysExist
// cannot see it.
func TestLDAPFieldsHaveLabels(t *testing.T) {
	for _, f := range directory.LDAPProfileFields() {
		if catalogues[defaultLang]["ldap.field."+f.Key] == "" {
			t.Errorf("profile field %q has no ldap.field label", f.Key)
		}
	}
}

// TestTranslate pins how a message renders: a key with arguments, an argument
// that is itself a key, the English fallback, and a string that is no key.
func TestTranslate(t *testing.T) {
	cases := []struct {
		lang, message, want string
	}{
		{"en", "common.cancel", "Cancel"},
		{"tr", "common.cancel", "İptal"},
		{"en", msg("common.requestFailed", "502"), "The request failed (HTTP 502)."},
		{"tr", msg("common.requestFailed", "502"), "İstek başarısız oldu (HTTP 502)."},
		{"tr", "carol@hermex.test", "carol@hermex.test"},
		{"xx", "common.cancel", "Cancel"},
	}
	for _, c := range cases {
		if got := translate(c.lang, c.message); got != c.want {
			t.Errorf("translate(%s, %q) = %q, want %q", c.lang, c.message, got, c.want)
		}
	}
	// A key argument renders in the page's language.
	if got := translate("tr", msg("common.requestFailed", "common.cancel")); got != "İstek başarısız oldu (HTTP İptal)." {
		t.Errorf("a key argument renders %q", got)
	}
}

// TestAcceptLanguage pins the browser-language fallback.
func TestAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"":                        "en",
		"tr-TR,tr;q=0.9,en;q=0.8": "tr",
		"de-DE,de;q=0.9,tr;q=0.8": "tr",
		"de-DE,fr;q=0.9":          "en",
		"en-US,en;q=0.9,tr;q=0.8": "en",
		"TR":                      "tr",
		" tr ; q=1 , en":          "tr",
	}
	for header, want := range cases {
		if got := acceptLanguage(header); got != want {
			t.Errorf("acceptLanguage(%q) = %q, want %q", header, got, want)
		}
	}
}

// loginPage fetches the sign-in page with the given language cookie and
// Accept-Language header and returns its body.
func loginPage(t *testing.T, ts *httptest.Server, cookie, accept string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/ui/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: langCookie, Value: cookie})
	}
	if accept != "" {
		req.Header.Set("Accept-Language", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestLoginPageLanguage proves the order a signed-out page picks its language
// in: the language cookie, then the browser's language, then English.
func TestLoginPageLanguage(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	turkish := catalogues["tr"]["login.submit"]
	english := catalogues["en"]["login.submit"]
	cases := []struct {
		name, cookie, accept, want string
	}{
		{"nothing named", "", "", english},
		{"browser language", "", "tr-TR,tr;q=0.9", turkish},
		{"cookie over browser", "en", "tr-TR", english},
		{"cookie alone", "tr", "", turkish},
		{"unsupported cookie", "de", "tr", turkish},
	}
	for _, c := range cases {
		body := loginPage(t, ts, c.cookie, c.accept)
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: the page does not say %q", c.name, c.want)
		}
	}
	if body := loginPage(t, ts, "tr", ""); !strings.Contains(body, `<html lang="tr"`) {
		t.Error("a Turkish page does not declare its language")
	}
}

// panelPage loads the dashboard as the signed-in operator with an optional
// language cookie, returning the response and its body.
func panelPage(t *testing.T, ts *httptest.Server, session, cookie string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/admin/ui/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: langCookie, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// langCookieOf returns the language cookie a response sets and whether it sets one.
func langCookieOf(resp *http.Response) (string, bool) {
	for _, c := range resp.Cookies() {
		if c.Name == langCookie {
			return c.Value, true
		}
	}
	return "", false
}

// TestStoredLanguageWins proves the users record decides a signed-in page's
// language over a stale cookie and refreshes that cookie, so a language chosen in
// webmail shows here, and that a cleared choice drops the cookie.
func TestStoredLanguageWins(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		uiPrefs: map[string]directory.UserPrefs{"admin@hermex.test": {Lang: "tr"}},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	resp, body := panelPage(t, ts, session, "en")
	if !strings.Contains(body, catalogues["tr"]["common.signOut"]) {
		t.Error("the stored Turkish lost to an English cookie")
	}
	if v, set := langCookieOf(resp); !set || v != "tr" {
		t.Errorf("the stale cookie was refreshed to %q (set %v), want tr", v, set)
	}
	if resp, _ := panelPage(t, ts, session, "tr"); func() bool { _, set := langCookieOf(resp); return set }() {
		t.Error("a matching cookie was set again")
	}

	d.uiPrefs["admin@hermex.test"] = directory.UserPrefs{}
	resp, body = panelPage(t, ts, session, "tr")
	if !strings.Contains(body, catalogues["en"]["common.signOut"]) {
		t.Error("a cleared choice still renders from the stale cookie")
	}
	if v, set := langCookieOf(resp); !set || v != "" {
		t.Errorf("a cleared choice left the cookie at %q (set %v), want it cleared", v, set)
	}
}

// TestUISavePrefsStoresTheLanguage proves the language selector stores the
// choice in the users record and caches it in the cookie, and refuses a language
// the panel does not ship.
func TestUISavePrefsStoresTheLanguage(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/prefs", session, csrf, url.Values{"lang": {"tr"}})
	resp.Body.Close()
	if v, _ := langCookieOf(resp); resp.StatusCode != http.StatusNoContent || v != "tr" {
		t.Fatalf("save = %d, cookie %q; want 204 and tr", resp.StatusCode, v)
	}
	if p := d.uiPrefs["admin@hermex.test"]; p.Lang != "tr" {
		t.Errorf("stored prefs = %+v, want the Turkish language", p)
	}
	bad := htmxPUT(t, ts, "/admin/ui/prefs", session, csrf, url.Values{"lang": {"de"}})
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest || d.uiPrefs["admin@hermex.test"].Lang != "tr" {
		t.Errorf("an unsupported language = %d, stored %q; want 400 and no change", bad.StatusCode, d.uiPrefs["admin@hermex.test"].Lang)
	}
	none := htmxPUT(t, ts, "/admin/ui/prefs", session, csrf, url.Values{})
	none.Body.Close()
	if none.StatusCode != http.StatusBadRequest {
		t.Errorf("a save naming nothing = %d, want 400", none.StatusCode)
	}
}
