package webmail2api

import (
	"net/http"
	"path"
	"path/filepath"
	"strings"
)

// assetPrefix is where the build puts its content-hashed bundles. A file under
// it is addressed by a name derived from its own contents, so its bytes can
// never change: a new build writes a new name, and the old name simply stops
// being referenced.
const assetPrefix = "/assets/"

// Cache-Control values for the two classes of file the SPA is made of.
//
// A hashed bundle is immutable by construction, so it is cached for a year and
// never revalidated, which is the whole point of hashing the name. Everything
// else, above all index.html, is mutable at a fixed address and must be
// revalidated before use: index.html is what names the current bundles, and a
// browser left to guess freshness from Last-Modified would go on requesting the
// bundle names of a build that no longer exists.
const (
	immutableCache  = "public, max-age=31536000, immutable"
	revalidateCache = "no-cache"
)

// spaHandler serves the built SPA from distDir: real files (assets, sw.js) are
// served directly, and a path with no matching file falls back to index.html so
// the SPA's client-side router resolves the route.
//
// The fallback is for routes only. A miss under the asset prefix is a real 404:
// answering it with index.html hands the browser an HTML page where it asked for
// a script, which then fails later and somewhere else than the file that is
// actually missing.
func spaHandler(distDir string) http.Handler {
	dir := http.Dir(distDir)
	files := http.FileServer(dir)
	index := filepath.Join(distDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean(r.URL.Path)
		asset := strings.HasPrefix(clean, assetPrefix)
		f, err := dir.Open(clean)
		if err != nil {
			if asset {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", revalidateCache)
			http.ServeFile(w, r, index)
			return
		}
		_ = f.Close()
		if asset {
			w.Header().Set("Cache-Control", immutableCache)
		} else {
			w.Header().Set("Cache-Control", revalidateCache)
		}
		files.ServeHTTP(w, r)
	})
}
