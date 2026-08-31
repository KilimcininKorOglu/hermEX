package admin

import (
	"net/http"
	"testing"
)

// TestAdminHTMLIsNoStore proves an admin UI page is marked no-store, so operator
// HTML never lands in a browser's disk cache or back/forward cache where a later
// user of a shared machine could read it.
func TestAdminHTMLIsNoStore(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	resp, err := http.Get(ts.URL + "/admin/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("admin HTML Cache-Control = %q, want no-store", got)
	}
}

// TestAdminStaticIsCacheable is the negative control: the blanket no-store must
// not reach /admin/static/, whose embedded assets carry no operator data and get
// a short cacheable policy plus a strong ETag.
func TestAdminStaticIsCacheable(t *testing.T) {
	ts := adminServer(t, &fakeDir{})
	resp, err := http.Get(ts.URL + "/admin/static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static asset status %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("static Cache-Control = %q, want public, max-age=3600", got)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("static asset carries no ETag")
	}

	// A conditional request with the served ETag revalidates to 304.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/static/style.css", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match match status %d, want 304", resp2.StatusCode)
	}
}
