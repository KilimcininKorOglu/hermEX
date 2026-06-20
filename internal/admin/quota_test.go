package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestAdminGetUserQuota proves a system admin reads a user's quota limits and
// current usage from the mailbox store.
func TestAdminGetUserQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{
		quota: map[string]objectstore.QuotaLimits{"/mb/alice": {SendKB: 1024, ReceiveKB: 2048, StorageKB: 4096}},
		used:  map[string]int64{"/mb/alice": 5 * 1024 * 1024},
	}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/quota", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get quota status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"receiveKB":2048`) || !strings.Contains(string(body), `"usedBytes":5242880`) {
		t.Errorf("quota body = %s, want the limits and usage", body)
	}
}

// TestAdminSetUserQuota proves a system admin writes a user's quota limits through
// to the mailbox store.
func TestAdminSetUserQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/quota", session, csrf,
		`{"sendKB":1024,"receiveKB":2048,"storageKB":4096}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set quota status %d, want 204", resp.StatusCode)
	}
	if store.setQuotaDir != "/mb/alice" || store.setQuotaVal.ReceiveKB != 2048 || store.setQuotaVal.StorageKB != 4096 {
		t.Errorf("stored quota = dir %q %+v, want /mb/alice with the submitted limits", store.setQuotaDir, store.setQuotaVal)
	}
}

// TestAdminUserQuotaNotFound proves quota read/write on an unknown user is a 404
// and the store is never written.
func TestAdminUserQuotaNotFound(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}, getUserMissing: true}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/ghost@hermex.test/quota", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/ghost@hermex.test/quota", session, csrf, `{"sendKB":1}`)
	put.Body.Close()
	if get.StatusCode != http.StatusNotFound || put.StatusCode != http.StatusNotFound {
		t.Errorf("unknown-user quota = GET %d / PUT %d, want both 404", get.StatusCode, put.StatusCode)
	}
	if store.setQuotaDir != "" {
		t.Errorf("an unknown user still wrote quota at %q", store.setQuotaDir)
	}
}

// TestAdminUserQuotaRequiresSystem proves a domain admin cannot read or write a
// user's quota through the system-scoped endpoints.
func TestAdminUserQuotaRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/quota", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/quota", session, csrf, `{"sendKB":1}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin quota = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsQuota proves the detail page renders the storage-quota
// section with the usage and the limits converted to MiB.
func TestUIUserDetailShowsQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{
		quota: map[string]objectstore.QuotaLimits{"/mb/alice": {ReceiveKB: 2048}}, // 2 MiB
		used:  map[string]int64{"/mb/alice": 3 * 1024 * 1024},                     // 3 MiB
	}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Storage quota", "Used space: 3 MiB", `name="receivemb" value="2"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page quota section missing %q", want)
		}
	}
}

// TestUIUserQuota proves the detail-form save converts the MiB inputs to KiB and
// writes them through to the store, reporting success.
func TestUIUserQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/quota", session, csrf, url.Values{
		"sendmb":    {"10"},
		"receivemb": {"20"},
		"storagemb": {"30"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui quota save status %d, want 200", resp.StatusCode)
	}
	got := store.setQuotaVal
	if store.setQuotaDir != "/mb/alice" || got.SendKB != 10*1024 || got.ReceiveKB != 20*1024 || got.StorageKB != 30*1024 {
		t.Errorf("stored quota = dir %q %+v, want the MiB inputs converted to KiB", store.setQuotaDir, got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui quota save did not report success:\n%s", body)
	}
}
