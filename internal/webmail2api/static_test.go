package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// builtSPA lays out a dist directory the way the build does: a mutable shell and
// a service worker at the root, and content-hashed bundles under assets/.
func builtSPA(t *testing.T) http.Handler {
	t.Helper()
	dist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.html":                `<!doctype html><script src="/assets/index-BzkJEwiv.js"></script>`,
		"sw.js":                     "self.addEventListener('install', () => {})",
		"assets/index-BzkJEwiv.js":  "console.log('app')",
		"assets/index-C0-j9O52.css": "body{}",
	} {
		if err := os.WriteFile(filepath.Join(dist, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return spaHandler(dist)
}

// getSPA issues one GET through the SPA handler.
func getSPA(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestHashedAssetsAreCachedForever proves the content-hashed bundles get the
// caching their naming scheme exists to enable. Without a header the browser
// falls back to guessing freshness from Last-Modified and revalidates files
// whose bytes can never change.
func TestHashedAssetsAreCachedForever(t *testing.T) {
	h := builtSPA(t)
	for _, p := range []string{"/assets/index-BzkJEwiv.js", "/assets/index-C0-j9O52.css"} {
		rec := getSPA(t, h, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200", p, rec.Code)
		}
		got := rec.Header().Get("Cache-Control")
		if !strings.Contains(got, "immutable") || !strings.Contains(got, "max-age=31536000") {
			t.Errorf("%s: Cache-Control = %q, want a long-lived immutable value", p, got)
		}
	}
}

// TestShellIsRevalidated is the other half, and the one that breaks the UI when
// it is missing. index.html is what names the current bundles; a browser that
// heuristically cached the previous one goes on requesting bundle names a rebuild
// has already replaced, and the webmail stays broken until that cache entry
// expires or the user hard-refreshes.
func TestShellIsRevalidated(t *testing.T) {
	h := builtSPA(t)
	// The shell, the service worker, and a client-side route that falls back to
	// the shell are all mutable at a fixed address. ("/index.html" is not in the
	// list: the file server redirects it to "/", which is what a browser asks
	// for anyway.)
	for _, p := range []string{"/sw.js", "/", "/mail/inbox"} {
		rec := getSPA(t, h, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p, got)
		}
	}
}

// TestRouteFallbackStillServesTheShell guards the behaviour the caching change
// sits on top of: an unknown path is a client-side route, not a missing file.
func TestRouteFallbackStillServesTheShell(t *testing.T) {
	rec := getSPA(t, builtSPA(t), "/settings/security")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("a client-side route did not get the shell: %q", rec.Body.String())
	}
}

// TestMissingAssetIs404 proves a bundle that is gone is reported as gone. The
// route fallback used to answer it with index.html, so a browser asking for a
// script received an HTML page with a 200 and failed later, somewhere other than
// the file that was actually missing. This is exactly the request a stale shell
// makes after a rebuild.
func TestMissingAssetIs404(t *testing.T) {
	rec := getSPA(t, builtSPA(t), "/assets/index-OldHash0.js")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("a missing bundle was answered with the app shell")
	}
}
