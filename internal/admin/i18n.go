package admin

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"hermex/internal/buildinfo"
	"hermex/internal/directory"
)

// localeFS holds the panel's message catalogues: one directory per language, each
// holding JSON files of nested keys, so a page group keeps its strings in a file
// of its own.
//
//go:embed locales/*/*.json
var localeFS embed.FS

// defaultLang is the language a request gets when nothing names a supported one.
const defaultLang = "en"

// langCookie caches the operator's language, so a panel fragment renders in the
// language of the page it lands in. The users record, which webmail shares, is
// where the choice is stored.
const langCookie = "admin_lang"

// msgSep joins a message key and its arguments into one string. A panel message
// travels as a string through the view models, and the template's t function
// splits it back apart, so a message composed from parts translates as a whole.
const msgSep = "\x1f"

// catalogues maps a language to its flattened keys. A malformed embedded file is
// a build-time bug, so loading panics, as template parsing does.
var catalogues = mustLoadCatalogues(localeFS)

// mustLoadCatalogues reads every locales/<lang>/*.json file into one flat
// catalogue per language.
func mustLoadCatalogues(fsys fs.FS) map[string]map[string]string {
	files, err := fs.Glob(fsys, "locales/*/*.json")
	if err != nil {
		panic(err)
	}
	out := map[string]map[string]string{}
	for _, name := range files {
		lang := path.Base(path.Dir(name))
		if out[lang] == nil {
			out[lang] = map[string]string{}
		}
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			panic(err)
		}
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			panic(fmt.Errorf("%s: %w", name, err))
		}
		if err := flattenInto(out[lang], "", tree); err != nil {
			panic(fmt.Errorf("%s: %w", name, err))
		}
	}
	return out
}

// flattenInto adds tree's leaves to flat under dotted keys, refusing a key two
// files define and a leaf that is not a string.
func flattenInto(flat map[string]string, prefix string, tree map[string]any) error {
	for k, v := range tree {
		key := prefix + k
		switch v := v.(type) {
		case string:
			if _, dup := flat[key]; dup {
				return fmt.Errorf("key %q is defined twice", key)
			}
			flat[key] = v
		case map[string]any:
			if err := flattenInto(flat, key+".", v); err != nil {
				return err
			}
		default:
			return fmt.Errorf("key %q holds a %T, not a string", key, v)
		}
	}
	return nil
}

// msg returns a panel message: a catalogue key and the arguments its {0}, {1}...
// placeholders take. An argument that is itself a catalogue key is translated;
// any other argument is shown as given.
func msg(key string, args ...string) string {
	return strings.Join(append([]string{key}, args...), msgSep)
}

// translate renders a message, a catalogue key or a msg result, in lang. A key the
// language lacks falls back to English, and a string that is no key at all is
// shown as it is, so a value that is not a message renders unchanged.
func translate(lang, message string, extra ...any) string {
	parts := strings.Split(message, msgSep)
	text, ok := lookup(lang, parts[0])
	if !ok {
		return message
	}
	args := make([]string, 0, len(parts)-1+len(extra))
	for _, a := range parts[1:] {
		args = append(args, translate(lang, a))
	}
	for _, a := range extra {
		args = append(args, fmt.Sprint(a))
	}
	for i, a := range args {
		text = strings.ReplaceAll(text, "{"+strconv.Itoa(i)+"}", a)
	}
	return text
}

// lookup returns key's text in lang, or in English when lang lacks it.
func lookup(lang, key string) (string, bool) {
	if v, ok := catalogues[lang][key]; ok {
		return v, true
	}
	v, ok := catalogues[defaultLang][key]
	return v, ok
}

// templateFuncs are the functions every template set shares; "t" and "lang" are
// bound to the set's language.
func templateFuncs(lang string) template.FuncMap {
	return template.FuncMap{
		"asset":   assetURL,
		"icon":    iconURL,
		"dict":    dict,
		"version": buildinfo.Display,
		"lang":    func() string { return lang },
		"t":       func(message string, args ...any) string { return translate(lang, message, args...) },
	}
}

// parseTemplates builds one template set per supported language. A parse failure
// is a build-time bug, so it panics.
func parseTemplates() map[string]*template.Template {
	sets := make(map[string]*template.Template, len(directory.UILanguages))
	for _, lang := range directory.UILanguages {
		sets[lang] = template.Must(template.New("").Funcs(templateFuncs(lang)).ParseFS(templateFS, "templates/*.html"))
	}
	return sets
}

// langKey carries the language the prefs middleware resolved from the users record.
type langKey struct{}

// withLang records the caller's stored language on the request.
func withLang(r *http.Request, lang string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), langKey{}, lang))
}

// requestLang picks the language a response renders in: the caller's stored
// choice, then the language cookie, then the browser's Accept-Language, then
// English.
func requestLang(r *http.Request) string {
	if r == nil {
		return defaultLang
	}
	if lang, ok := r.Context().Value(langKey{}).(string); ok && supportedLang(lang) {
		return lang
	}
	if c, err := r.Cookie(langCookie); err == nil && supportedLang(c.Value) {
		return c.Value
	}
	return acceptLanguage(r.Header.Get("Accept-Language"))
}

// supportedLang reports whether the panel ships a catalogue for lang.
func supportedLang(lang string) bool {
	return slices.Contains(directory.UILanguages, lang)
}

// acceptLanguage returns the first supported language an Accept-Language header
// names, in the order the browser lists them, or English. Quality values only
// order what the browser already lists first, so they are not weighed.
func acceptLanguage(header string) string {
	for part := range strings.SplitSeq(header, ",") {
		tag, _, _ := strings.Cut(part, ";")
		base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if supportedLang(base) {
			return base
		}
	}
	return defaultLang
}
