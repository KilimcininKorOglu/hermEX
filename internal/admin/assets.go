package admin

import (
	"embed"
	"html/template"
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
// so it panics.
var tmpl = template.Must(template.New("").Funcs(template.FuncMap{"asset": assetURL}).ParseFS(templateFS, "templates/*.html"))

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
