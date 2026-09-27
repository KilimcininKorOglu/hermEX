package admin

import (
	"embed"
	"errors"
	"html/template"

	"hermex/internal/buildinfo"
)

// templateFS holds the admin UI HTML templates compiled into the binary.
//
//go:embed templates/*.html
var templateFS embed.FS

// staticFS holds the static assets served under /admin/static/.
//
//go:embed static/*
var staticFS embed.FS

// staticAssets holds every embedded static file with its content hash, computed
// once at startup. The templates and staticHandler both read it, so the version a
// page links to is always the hash of the bytes the handler serves.
var staticAssets = buildStaticAssets()

// tmpl is the parsed admin UI template set. A parse failure is a build-time bug,
// so it panics. "version" is the panel binary's own release and commit, shown
// under the sidebar so an operator can tell which build the panel runs.
var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"asset":   assetURL,
	"icon":    iconURL,
	"dict":    dict,
	"version": buildinfo.Display,
}).ParseFS(templateFS, "templates/*.html"))

// errDictArgs reports a dict call whose arguments are not key and value pairs.
var errDictArgs = errors.New("dict wants string keys each followed by a value")

// dict builds the map a shared sub-template takes, from alternating keys and
// values, so a page can hand a card header its icon, title and description.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, errDictArgs
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, errDictArgs
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// iconURL returns the reference an <svg><use> takes for one symbol of the icon
// sprite, versioned like every other static file.
func iconURL(name string) string {
	return assetURL("icons.svg") + "#" + name
}

// assetVersionLen is how many hex digits of the content hash an asset URL
// carries: enough that two builds of one file never share a version.
const assetVersionLen = 16

// assetURL returns the URL a page links a static file by, carrying a version
// derived from its content. A changed file gets a new URL, so a browser holding
// the old one fetches the new bytes instead of trusting its cached copy. A name
// with no embedded file is returned unversioned, and the handler answers it 404.
func assetURL(name string) string {
	a, ok := staticAssets[name]
	if !ok {
		return "/admin/static/" + name
	}
	return "/admin/static/" + name + "?v=" + a.version
}
