package webmail2api

import (
	"net/http"
	"path"
	"path/filepath"
)

// spaHandler serves the built SPA from distDir: real files (assets, sw.js) are
// served directly, and any path without a matching file falls back to index.html
// so the SPA's client-side router resolves the route.
func spaHandler(distDir string) http.Handler {
	dir := http.Dir(distDir)
	files := http.FileServer(dir)
	index := filepath.Join(distDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := dir.Open(path.Clean(r.URL.Path))
		if err != nil {
			http.ServeFile(w, r, index)
			return
		}
		_ = f.Close()
		files.ServeHTTP(w, r)
	})
}
